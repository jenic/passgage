package agecrypto_test

import (
	"filippo.io/age"
	"fmt"

	"github.com/jenic/passgage/agecrypto"
)

func ExampleEngine() {
	id, err := age.GenerateX25519Identity()
	if err != nil {
		panic(err)
	}
	engine, err := agecrypto.New(agecrypto.Config{Identities: []age.Identity{id}})
	if err != nil {
		panic(err)
	}
	cipher, err := engine.Encrypt([]byte("example"), id.Recipient().String())
	if err != nil {
		panic(err)
	}
	plain, err := engine.Decrypt(cipher)
	if err != nil {
		panic(err)
	}
	defer clear(plain)
	fmt.Println(string(plain))
	// Output: example
}
