package main

import "fmt"

// Conditons in Go
func main() {
	fmt.Println("Conditions in Go")
	var age int = 18
	if age >= 18 {
		fmt.Println("You can vote!")
	} else {
		fmt.Println("You cannot vote!")
	}
}
