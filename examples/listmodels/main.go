// Command listmodels prints the models available to the account. Set
// TYPESAFE_API_KEY before running.
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/klnstprx/typesafe-sdk-go"
)

func main() {
	client, err := typesafe.New()
	if err != nil {
		log.Fatal(err)
	}
	defer client.Close()

	resp, err := client.Models.List(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	for _, m := range resp.Models {
		fmt.Printf("%-16s %s  %s\n", m.Name, m.ReleaseDate, m.Description)
	}
}
