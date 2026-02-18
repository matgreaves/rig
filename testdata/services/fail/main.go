// fail exits immediately with a non-zero status. Used for testing failure propagation.
package main

import "os"

func main() {
	os.Exit(1)
}
