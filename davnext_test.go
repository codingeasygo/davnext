package main

import (
	"io/ioutil"
	"os"
	"testing"
)

func TestRead(t *testing.T) {
	prop := NewPropfind()
	data, _ := ioutil.ReadFile("t.xml")
	prop.Append(string(data))
	prop.Append(string(data))
	prop.WriteTo(os.Stdout)
}
