package main

import "fmt"

// A constant is avariable with a value that can't be changed
// LoginToken is a global variable and can be accessed throughout the package 02variables
const LoginToken string = "ae3$ls@45s1" // Public, package scoped

func main() {

	// Using public constant
	fmt.Println(LoginToken)
	fmt.Printf("Variable is of type: %T \n", LoginToken)
	// Get the length of the string
	fmt.Println("Length of string is", len(LoginToken))

	// Multiple declaration
	var (
		a = 1
		b = 2
		c = 3
	)
	fmt.Println(a, b, c)
}
