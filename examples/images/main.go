// Command images locates the damage in a product photo with a box question.
// Set TYPESAFE_API_KEY and pass a PNG or JPEG file:
//
//	go run ./examples/images photo.jpg
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"

	"github.com/klnstprx/typesafe-sdk-go"
)

func main() {
	if len(os.Args) != 2 {
		log.Fatal("usage: images PHOTO")
	}
	data, err := os.ReadFile(os.Args[1])
	if err != nil {
		log.Fatal(err)
	}
	// Image support is experimental; the API requires this header to use it.
	client, err := typesafe.New(typesafe.WithHeader("TypeSafe-Danger-Zone-Images", "true"))
	if err != nil {
		log.Fatal(err)
	}
	defer client.Close()
	if err := run(context.Background(), client, data, os.Stdout); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context, client *typesafe.Client, data []byte, w io.Writer) error {
	photo, err := typesafe.NewImage(data)
	if err != nil {
		return err
	}
	resp, err := client.SystemOne(ctx, photo, typesafe.Questions{
		"damage":  typesafe.Box{Instructions: "The damaged area of the product"},
		"damaged": typesafe.Noul{Instructions: "Is the product in the photo damaged?"},
	}, typesafe.WithModel("jev-preview"))
	if err != nil {
		return err
	}
	damaged, ok := resp.Nouls()["damaged"]
	if !ok {
		return errors.New("the damaged answer is missing")
	}
	damage, ok := resp.Boxes()["damage"]
	if !ok {
		return errors.New("the damage answer is missing")
	}
	b := damage.Box
	fmt.Fprintf(w, "damaged: %.2f\n", damaged.Noul)
	fmt.Fprintf(w, "damage:  top %.2f, bottom %.2f, left %.2f, right %.2f of the image\n", b.Top, b.Bottom, b.Left, b.Right)
	if damage.Confidence != nil {
		fmt.Fprintf(w, "confidence: %.2f\n", *damage.Confidence)
	}
	return nil
}
