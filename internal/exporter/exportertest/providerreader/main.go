// A subprocess protocol fixture, deliberately independent of exporter code.
package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

var fixture string

func main() {
	if len(os.Args) > 1 && os.Args[1] == "fixture-child" {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			os.Exit(4)
		}
		os.WriteFile(filepath.Join(fixture, "child-address"), []byte(listener.Addr().String()), 0600)
		os.WriteFile(filepath.Join(fixture, "child-pid"), []byte(strconv.Itoa(os.Getpid())), 0600)
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			connection.Write([]byte("alive"))
			connection.Close()
		}
	}

	cwd, _ := os.Getwd()
	rec, _ := os.OpenFile(filepath.Join(fixture, "calls"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	cli, _ := os.ReadFile(os.Getenv("TF_CLI_CONFIG_FILE"))
	lock, _ := os.ReadFile(".terraform.lock.hcl")
	archive := filepath.Join(cwd, "mirror", "registry.terraform.io", "pingidentity", "pingone", "terraform-provider-pingone_1.24.0_"+runtime.GOOS+"_"+runtime.GOARCH+".zip")
	providerArchive, _ := os.ReadFile(archive)
	modes := map[string]uint32{}
	filepath.WalkDir(cwd, func(p string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		info, e := d.Info()
		if e != nil {
			return e
		}
		rel, _ := filepath.Rel(cwd, p)
		modes[rel] = uint32(info.Mode().Perm())
		return nil
	})
	json.NewEncoder(rec).Encode(map[string]any{"argv": os.Args[1:], "env": os.Environ(), "dir": cwd, "bin": os.Args[0], "cli": string(cli), "lock": string(lock), "archive": string(providerArchive), "modes": modes})
	rec.Close()
	mode, _ := os.ReadFile(filepath.Join(fixture, "mode"))
	args := strings.Join(os.Args[1:], " ")
	if string(mode) == "descendant" || string(mode) == "descendant-failure" {
		child := exec.Command(os.Args[0], "fixture-child")
		child.Env = os.Environ()
		child.Stdout = os.Stdout
		child.Stderr = os.Stderr
		if child.Start() != nil {
			os.Exit(5)
		}
		if string(mode) == "descendant-failure" {
			for i := 0; i < 100; i++ {
				if _, err := os.Stat(filepath.Join(fixture, "child-address")); err == nil {
					os.Exit(2)
				}
				time.Sleep(10 * time.Millisecond)
			}
			os.Exit(6)
		}
		time.Sleep(10 * time.Second)
	}
	if string(mode) == "timeout" {
		time.Sleep(10 * time.Second)
	}
	if args == "--version" {
		if string(mode) == "version" {
			fmt.Println("Terraform v1.16.6")
		} else {
			fmt.Println("Terraform v1.16.7")
		}
		return
	}
	if string(mode) == "overflow" {
		fmt.Print(strings.Repeat("S", 1048576))
		return
	}
	if strings.HasPrefix(args, "init ") {
		return
	}
	if args == "providers schema -json" {
		fmt.Print(`{"provider_schemas":{"registry.terraform.io/pingidentity/pingone":{"resource_schemas":{"pingone_population":{"block":{"attributes":{"environment_id":{"type":"string","required":true},"name":{"type":"string","required":true},"id":{"type":"string","computed":true},"computed_secret":{"type":"string","computed":true,"sensitive":true}}}}}}}}`)
		return
	}
	if len(os.Args) == 6 && os.Args[1] == "import" && os.Args[2] == "-input=false" && os.Args[3] == "-no-color" {
		if string(mode) == "failure" {
			fmt.Fprint(os.Stderr, "CLIENT_SECRET_AND_PROVIDER_SECRET")
			os.Exit(2)
		}
		f, _ := os.OpenFile("imports.json", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		json.NewEncoder(f).Encode(map[string]string{"address": os.Args[4], "id": os.Args[5]})
		f.Close()
		return
	}
	if args == "show -json" {
		f, _ := os.Open("imports.json")
		d := json.NewDecoder(f)
		var resources []any
		for {
			var item map[string]string
			if d.Decode(&item) != nil {
				break
			}
			typ, label, _ := strings.Cut(item["address"], ".")
			_ = label
			resources = append(resources, map[string]any{"address": item["address"], "mode": "managed", "type": typ, "values": map[string]any{"environment_id": strings.Split(item["id"], "/")[0], "name": "Population", "id": "resource-id", "computed_secret": "COMPUTED_SECRET_NEVER_EXPORT"}, "sensitive_values": map[string]any{"computed_secret": true}})
		}
		json.NewEncoder(os.Stdout).Encode(map[string]any{"values": map[string]any{"root_module": map[string]any{"resources": resources}}})
		return
	}
	fmt.Fprint(os.Stderr, "unexpected command")
	os.Exit(3)
}
