package main

import "fmt"

// A pointer is a variable which stores the memory address of another variable
func main() {
	var username string = "Vidyesh"
	fmt.Println(username)
	var usernamePointer *string = &username
	fmt.Println(usernamePointer)
	fmt.Println(*usernamePointer)

	// Change the value of username using the usernamePointer
	*usernamePointer = "Vidyesh Churi"
	fmt.Println(*usernamePointer)
	fmt.Println(username)
}
