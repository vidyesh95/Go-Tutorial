package main

import "fmt"

// Go has 4 types of data types namely string, boolean, numeric(integer and float) and derived( array, pointer, struct, function, interface, slice, channel, map)
func main() {
	var username string = "vidyesh"
	fmt.Println(username)
	fmt.Printf("Variable is of type: %T \n", username)

	var isLoggedIn bool = false
	fmt.Println(isLoggedIn)
	fmt.Printf("Variable is of type: %T \n", isLoggedIn)

	var smallVal uint8 = 255
	fmt.Println(smallVal)
	fmt.Printf("Variable is of type: %T \n", smallVal)

	var smallFloat float32 = 255.45544
	fmt.Println(smallFloat)
	fmt.Printf("Variable is of type: %T \n", smallFloat)

	// Default values and some aliases
	var anotherVariable int
	fmt.Println(anotherVariable)
	fmt.Printf("Variable is of type: %T \n", anotherVariable)

	// Implicit type
	var website = "qatoto.com"
	fmt.Println(website)
	fmt.Printf("Variable is of type: %T \n", website)

	// No var style
	numberOfUsers := 300000
	fmt.Println(numberOfUsers)
	fmt.Printf("Variable is of type: %T \n", numberOfUsers)
}
