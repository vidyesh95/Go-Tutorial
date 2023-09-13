package main

import "fmt"

// Go has 4 types of data types namely string, boolean, numeric(int[8,16,32,64] and float[32 and 64]) and derived( array, pointer, struct, function, interface, slice, channel, map)
func main() {
	var username string = "vidyesh"
	fmt.Println(username)
	fmt.Printf("Variable is of type: %T \n", username)

	var isLoggedIn bool = false
	fmt.Println(isLoggedIn)
	fmt.Printf("Variable is of type: %T \n", isLoggedIn)

	/*
	* The int, uint, and uintptr types are usually 32 bits wide on 32-bit systems and 64 bits wide on 64-bit systems.
	* When you need an integer value you should use int unless you have a specific reason to use a sized or unsigned integer type.
	 */
	var unsignedVal uint = 255
	fmt.Println(unsignedVal)
	fmt.Printf("Variable is of type: %T \n", unsignedVal)

	var byteValue uint8 = 255
	fmt.Println(byteValue)
	fmt.Printf("Variable is of type: %T \n", byteValue)

	var uint16Val uint16 = 255
	fmt.Println(uint16Val)
	fmt.Printf("Variable is of type: %T \n", uint16Val)

	var uint32Val uint32 = 255
	fmt.Println(uint32Val)
	fmt.Printf("Variable is of type: %T \n", uint32Val)

	var uint64Val uint64 = 255
	fmt.Println(uint64Val)
	fmt.Printf("Variable is of type: %T \n", uint64Val)

	/*
	* uintptr is generally used to perform indirect arithmetic operations on unsafe pointers for unsafe memory access.
	* First, unsafe.Pointer is converted to uintptr, and then the arithmetic operation is performed on uintptr.
	* Finally, it is converted back to unsafe.Pointer, which now points to the new memory address as a result of the operation.
	* uintptr is also used to store and print the pointer address value.
	* Since the variable with the type uintptr does not reference anything as it’s a plain integer, the pointer corresponding to that object can be garbage collected.
	 */
	var uintPtrVal uintptr = 0xc82000c290
	fmt.Println(uintPtrVal)
	fmt.Printf("Variable is of type: %T \n", uintPtrVal)

	var signedVal int = 255
	fmt.Println(signedVal)
	fmt.Printf("Variable is of type: %T \n", signedVal)

	var intVal int8 = -128
	fmt.Println(intVal)
	fmt.Printf("Variable is of type: %T \n", intVal)

	var int16Val int16 = -32768
	fmt.Println(int16Val)
	fmt.Printf("Variable is of type: %T \n", int16Val)

	var int32Val int32 = -2147483648
	fmt.Println(int32Val)
	fmt.Printf("Variable is of type: %T \n", int32Val)

	var int64Val int64 = -9223372036854775808
	fmt.Println(int64Val)
	fmt.Printf("Variable is of type: %T \n", int64Val)

	var float32Val float32 = 255.45544
	fmt.Println(float32Val)
	fmt.Printf("Variable is of type: %T \n", float32Val)

	var float64Val float64 = 255.12345678912345
	fmt.Println(float64Val)
	fmt.Printf("Variable is of type: %T \n", float64Val)

	/*
	* Default values: Variables declared without an explicit initial value are given their zero value.
	* The zero value is:
	* 0 for numeric types,
	* false for the boolean type, and
	* "" (the empty string) for strings.
	 */
	var anotherVariable int
	fmt.Println(anotherVariable)
	fmt.Printf("Variable is of type: %T \n", anotherVariable)

	// byte is an alias for uint8
	var byteVal byte = 255
	fmt.Println(byteVal)
	fmt.Printf("Variable is of type: %T \n", byteVal)

	// rune is an alias for int32 and represents a Unicode code point
	var runeVal rune = -214748364
	fmt.Println(runeVal)
	fmt.Printf("Variable is of type: %T \n", runeVal)

	// Implicit type
	var website = "qatoto.com"
	fmt.Println(website)
	fmt.Printf("Variable is of type: %T \n", website)

	// No var style
	numberOfUsers := 300000
	fmt.Println(numberOfUsers)
	fmt.Printf("Variable is of type: %T \n", numberOfUsers)

	// Type conversion
	var a int = 10
	var b float64 = 10.5
	fmt.Println(float64(a) + b)
	fmt.Println(a + int(b))
}
