package main

import "fmt"

// Conditons in Go
func main() {
	fmt.Println("Conditions in Go")
	var age int = 20

	if age >= 18 {
		fmt.Println("You can vote!")
	} else {
		fmt.Println("You cannot vote!")
	}

	switch age {
	case 16:
		fmt.Println("You can drive!")
	case 18:
		fmt.Println("You can vote!")
	case 21:
		fmt.Println("You can drink!")
	default:
		fmt.Println("You can have fun!")
	}
}
