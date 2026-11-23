package main

import (
	"fmt"
	"maps"
)

/**
* Maps
* A map maps keys to values.
* The zero value of a map is nil.
* A nil map has no keys, nor can keys be added.
* The make function returns a map of the given type, initialized and ready for use.
 */
func main() {
	map1 := make(map[string]int)
	map1["key1"] = 7
	map1["key2"] = 13
	fmt.Println("map1:", map1)
	fmt.Println("len:", len(map1))
	fmt.Println("key1:", map1["key1"])

	value1 := map1["key1"]
	fmt.Println("value1:", value1)

	value3 := map1["key3"]
	fmt.Println("value3:", value3)

	delete(map1, "key2")
	fmt.Println("map1:", map1)

	clear(map1)
	fmt.Println("map1:", map1)

	_, prs := map1["key2"]
	fmt.Println("prs:", prs)

	map2 := map[string]int{"foo": 1, "bar": 2}
	fmt.Println("map2:", map2)

	map3 := map[string]int{"foo": 1, "bar": 2}
	if maps.Equal(map2, map3) {
		fmt.Println("map2 == map3")
	} else {
		fmt.Println("map2 != map3")
	}
}
