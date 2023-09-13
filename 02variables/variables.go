package main

import "fmt"

// A constant is avariable with a value that can't be changed
// LoginToken is a global variable and can be accessed throughout the package 02variables
const LoginToken string = "ae3$ls@45s1" // Public, package scoped

/*
* The var statement declares a list of variables; as in function argument lists, the type is last.
* A var statement can be at package or function level. We see both in this example.
* A var declaration can include initializers, one per variable.
* If an initializer is present, the type can be omitted; the variable will take the type of the initializer.
* Inside a function, the := short assignment statement can be used in place of a var declaration with implicit type.
* Outside a function, every statement begins with a keyword (var, func, and so on) and so the := construct is not available.
 */
func main() {
	// Using public constant
	fmt.Println(LoginToken)
	fmt.Printf("Variable is of type: %T \n", LoginToken)
	// Get the length of the string
	fmt.Println("Length of string is", len(LoginToken))

	// Declaring a variable and assigning a value
	var username string = "vidyesh"
	fmt.Println(username)

	// Short variable declaration
	// This can be used only inside a function
	k := 10
	fmt.Printf("Type: %T Value: %v\n", k, k)

	// Multiple declaration
	var (
		a = 1
		b = 2
		c = 3
	)
	fmt.Println(a, b, c)
}
