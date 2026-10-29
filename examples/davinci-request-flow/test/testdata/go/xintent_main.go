// Intent cross-check (scratch copy only): decodes each fixture with the
// runner's own gitops.DecodeIntent and prints whether it is accepted.
package main

import (
	"encoding/json"
	"os"
	"strings"

	"pingone-promote/internal/gitops"
)

type fx struct {
	Name string `json:"name"`
	JSON string `json:"json"`
}

type out struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

func main() {
	var all []fx
	if err := json.NewDecoder(os.Stdin).Decode(&all); err != nil {
		panic(err)
	}
	res := []out{}
	for _, f := range all {
		_, err := gitops.DecodeIntent(strings.NewReader(f.JSON))
		if err != nil {
			res = append(res, out{false, err.Error()})
		} else {
			res = append(res, out{true, ""})
		}
	}
	if err := json.NewEncoder(os.Stdout).Encode(res); err != nil {
		panic(err)
	}
}
