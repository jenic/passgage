package gitstore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/plumbing/transport/http"
	gitssh "github.com/go-git/go-git/v5/plumbing/transport/ssh"
	"github.com/kevinburke/ssh_config"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

func endpoint(raw string) (*transport.Endpoint, error) {
	e, err := transport.NewEndpoint(raw)
	if err != nil {
		return nil, err
	}
	if e.Protocol != "https" && e.Protocol != "ssh" {
		return nil, fmt.Errorf("only native HTTPS and SSH remotes are supported")
	}
	if e.Password != "" {
		return nil, fmt.Errorf("tokens in remote URLs are unsupported; use PASSGAGE_GIT_TOKEN")
	}
	return e, nil
}
func auth(raw string, prompt func(string) (string, error)) (transport.AuthMethod, error) {
	e, err := endpoint(raw)
	if err != nil {
		return nil, err
	}
	if e.Protocol == "https" {
		token := os.Getenv("PASSGAGE_GIT_TOKEN")
		if token == "" {
			return nil, nil
		}
		user := os.Getenv("PASSGAGE_GIT_USER")
		if user == "" {
			user = "git"
		}
		return &http.BasicAuth{Username: user, Password: token}, nil
	}
	if _, err := ssh_config.GetStrict(e.Host, "HostName"); err != nil {
		return nil, fmt.Errorf("parse SSH configuration: %w", err)
	}
	for _, key := range []string{"ProxyCommand", "ProxyJump"} {
		v := ssh_config.Get(e.Host, key)
		if v != "" && v != "none" {
			return nil, fmt.Errorf("SSH %s is unsupported without external processes", key)
		}
	}
	user := e.User
	if user == "" {
		user = ssh_config.Get(e.Host, "User")
	}
	if user == "" {
		user = "git"
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	expand := func(p string) string {
		if strings.HasPrefix(p, "~/") {
			return filepath.Join(home, p[2:])
		}
		return p
	}
	known := ssh_config.Get(e.Host, "UserKnownHostsFile")
	if known == "" {
		known = filepath.Join(home, ".ssh", "known_hosts")
	}
	var knownFiles []string
	for _, p := range strings.Fields(known) {
		p = expand(p)
		if _, err := os.Stat(p); err == nil {
			knownFiles = append(knownFiles, p)
		} else if !os.IsNotExist(err) {
			return nil, err
		}
	}
	if len(knownFiles) == 0 {
		return nil, fmt.Errorf("no known_hosts file is available; configure trusted SSH host keys first")
	}
	cb, err := gitssh.NewKnownHostsCallback(knownFiles...)
	if err != nil {
		return nil, err
	}
	key := os.Getenv("PASSGAGE_GIT_SSH_KEY")
	agentAddress := ssh_config.Get(e.Host, "IdentityAgent")
	if key == "" && ssh_config.Get(e.Host, "IdentitiesOnly") != "yes" && agentAddress != "none" {
		var a *gitssh.PublicKeysCallback
		var e error
		if agentAddress != "" && agentAddress != "SSH_AUTH_SOCK" {
			conn, err := dialAgent(expand(agentAddress))
			e = err
			if err == nil {
				a = &gitssh.PublicKeysCallback{User: user, Callback: agent.NewClient(conn).Signers}
			}
		} else {
			a, e = gitssh.NewSSHAgentAuth(user)
		}
		if e == nil {
			signers, err := a.Callback()
			if err == nil && len(signers) > 0 {
				a.HostKeyCallback = cb
				return timedSSH{a}, nil
			}
		}
	}
	if key == "" || (key == "~/.ssh/identity" && missingFile(expand(key))) {
		key = ssh_config.Get(e.Host, "IdentityFile")
	}
	if key == "" {
		key = filepath.Join(home, ".ssh", "id_ed25519")
		if _, err = os.Stat(key); os.IsNotExist(err) {
			key = filepath.Join(home, ".ssh", "id_rsa")
		}
	}
	a, err := gitssh.NewPublicKeysFromFile(user, expand(key), "")
	if err != nil {
		if _, e := os.Stat(expand(key)); e != nil {
			return nil, e
		}
		pass, e := prompt("SSH key passphrase: ")
		if e != nil {
			return nil, e
		}
		a, err = gitssh.NewPublicKeysFromFile(user, expand(key), pass)
	}
	if err != nil {
		return nil, err
	}
	a.HostKeyCallback = cb
	return timedSSH{a}, nil
}

func missingFile(p string) bool { _, err := os.Stat(p); return os.IsNotExist(err) }

type timedSSH struct{ gitssh.AuthMethod }

func (a timedSSH) ClientConfig() (*ssh.ClientConfig, error) {
	c, err := a.AuthMethod.ClientConfig()
	if err == nil {
		c.Timeout = 30 * time.Second
	}
	return c, err
}

// Network runs fetch, fast-forward pull, or non-forced push.
func Network(r *git.Repository, args []string, prompt func(string) (string, error)) error {
	if len(args) < 1 || len(args) > 2 {
		return fmt.Errorf("usage: git <fetch|pull|push> [remote]")
	}
	name := "origin"
	if len(args) == 2 {
		name = args[1]
	}
	remote, err := r.Remote(name)
	if err != nil {
		return err
	}
	urls := remote.Config().URLs
	if len(urls) != 1 {
		return fmt.Errorf("exactly one remote URL is required")
	}
	a, err := auth(urls[0], prompt)
	if err != nil {
		return err
	}
	run := func(a transport.AuthMethod) error {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		switch args[0] {
		case "fetch":
			return r.FetchContext(ctx, &git.FetchOptions{RemoteName: name, Auth: a})
		case "pull":
			w, e := worktree(r)
			if e != nil {
				return e
			}
			status, e := w.Status()
			if e != nil {
				return e
			}
			if !status.IsClean() {
				return fmt.Errorf("pull requires a clean working tree and index")
			}
			return w.PullContext(ctx, &git.PullOptions{RemoteName: name, Auth: a})
		case "push":
			return r.PushContext(ctx, &git.PushOptions{RemoteName: name, Auth: a})
		default:
			return fmt.Errorf("unsupported network command")
		}
	}
	err = run(a)
	if errors.Is(err, transport.ErrAuthenticationRequired) && a == nil {
		e, _ := endpoint(urls[0])
		if e.Protocol == "https" {
			token, e := prompt("HTTPS token: ")
			if e != nil {
				return e
			}
			err = run(&http.BasicAuth{Username: "git", Password: token})
		}
	}
	if err == git.NoErrAlreadyUpToDate {
		return nil
	}
	return err
}

// Clone clones a remote into an empty destination using native transports.
func Clone(dir, url string, prompt func(string) (string, error)) error {
	entries, e := os.ReadDir(dir)
	if e != nil && !os.IsNotExist(e) {
		return e
	}
	if len(entries) > 0 {
		return fmt.Errorf("clone destination must be empty")
	}
	a, err := auth(url, prompt)
	if err != nil {
		return err
	}
	run := func(a transport.AuthMethod) error {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		_, e := git.PlainCloneContext(ctx, dir, false, &git.CloneOptions{URL: url, Auth: a})
		return e
	}
	err = run(a)
	if errors.Is(err, transport.ErrAuthenticationRequired) && a == nil {
		ep, _ := endpoint(url)
		if ep.Protocol == "https" {
			token, e := prompt("HTTPS token: ")
			if e != nil {
				return e
			}
			user := os.Getenv("PASSGAGE_GIT_USER")
			if user == "" {
				user = "git"
			}
			err = run(&http.BasicAuth{Username: user, Password: token})
		}
	}
	return err
}
