// Command p1-native-poc is a proof of concept of promotions through
// PingOne's native Configuration Management API from a GitLab pipeline.
//
//	p1-native-poc plan     --config promote.yml --request promotions/CIAM-77.yml --out plan
//	p1-native-poc apply    --plan plan
//	p1-native-poc rollback --plan plan --out rollback   (then: apply --plan rollback)
//	p1-native-poc demo     --out demo                   (no credentials: a mock API)
//
// Credentials come from PINGONE_CLIENT_ID and PINGONE_CLIENT_SECRET.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"pingone-promote/internal/nativepoc"
	"pingone-promote/internal/nativepoc/mockapi"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "p1-native-poc:", err)
		os.Exit(1)
	}
}

func client(cfg nativepoc.Config) (*nativepoc.Client, error) {
	id, secret := os.Getenv("PINGONE_CLIENT_ID"), os.Getenv("PINGONE_CLIENT_SECRET")
	if id == "" || secret == "" {
		return nil, errors.New("set PINGONE_CLIENT_ID and PINGONE_CLIENT_SECRET (masked CI variables)")
	}
	region := cfg.Region
	if region == "" {
		region = "com"
	}
	return &nativepoc.Client{APIBase: "https://api.pingone." + region, AuthBase: "https://auth.pingone." + region,
		WorkerEnv: cfg.WorkerEnvironment, ClientID: id, Secret: secret}, nil
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("use plan, apply, rollback or demo")
	}
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	cfgPath := fs.String("config", "promote.yml", "pipeline settings")
	reqPath := fs.String("request", "", "the promotion request file")
	planDir := fs.String("plan", "", "directory of a reviewed plan")
	out := fs.String("out", "", "directory to write the plan into")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	if args[0] == "demo" {
		return demo(ctx, *out)
	}
	var cfg nativepoc.Config
	if err := nativepoc.Load(*cfgPath, &cfg); err != nil {
		return err
	}
	c, err := client(cfg)
	if err != nil {
		return err
	}
	switch args[0] {
	case "plan":
		var req nativepoc.Request
		if err := nativepoc.Load(*reqPath, &req); err != nil {
			return err
		}
		r, err := nativepoc.Plan(ctx, c, cfg, req)
		if err != nil {
			return err
		}
		return report(*out, r)
	case "apply":
		r, err := nativepoc.Read(*planDir)
		if err != nil {
			return err
		}
		p, err := nativepoc.Apply(ctx, c, r)
		if err != nil {
			return err
		}
		fmt.Printf("promotion %s: %s\n", p.ID, p.Status)
		return nil
	case "rollback":
		r, err := nativepoc.Read(*planDir)
		if err != nil {
			return err
		}
		rb, err := nativepoc.PlanRollback(ctx, c, r)
		if err != nil {
			return err
		}
		return report(*out, rb)
	}
	return errors.New("use plan, apply, rollback or demo")
}

func report(dir string, r nativepoc.Review) error {
	if dir == "" {
		return errors.New("--out is required")
	}
	if err := nativepoc.Write(dir, r); err != nil {
		return err
	}
	c, u, d := r.Counts()
	fmt.Printf("%s -> %s: %d created, %d updated, %d deleted, %d warnings; see %s\n", r.Source.Name, r.Target.Name, c, u, d, len(r.Warnings), filepath.Join(dir, "report.html"))
	return nil
}

// demo runs plan, a refused stale apply, apply and a rollback plan against
// the mock API, writing each plan under dir.
func demo(ctx context.Context, dir string) error {
	if dir == "" {
		return errors.New("--out is required")
	}
	m := mockapi.Demo()
	defer m.Close()
	c := &nativepoc.Client{APIBase: m.URL, AuthBase: m.URL, WorkerEnv: "worker", ClientID: "demo", Secret: "demo", Poll: time.Millisecond}
	cfg := nativepoc.Config{Environments: map[string]string{"dev": mockapi.DevID, "qa": mockapi.QAID}}
	var req nativepoc.Request
	req.Ticket, req.Source, req.Target = "CIAM-77", "dev", "qa"
	req.Description = "Step-up MFA for risky sign-ons."
	req.Resources = append(req.Resources, struct {
		Type string `yaml:"type"`
		Name string `yaml:"name"`
	}{"flow", "CIAM Sign On"})

	fmt.Println("1. plan (nothing changes in qa)")
	plan, err := nativepoc.Plan(ctx, c, cfg, req)
	if err != nil {
		return err
	}
	if err := report(filepath.Join(dir, "1-plan"), plan); err != nil {
		return err
	}

	fmt.Println("2. someone edits qa by hand after the review; apply is refused")
	m.Edit(mockapi.QAID, "q-mfa", "value", "maybe")
	if _, err := nativepoc.Apply(ctx, c, plan); err == nil {
		return errors.New("the stale plan was applied")
	} else {
		fmt.Println("   refused:", err)
	}
	m.Edit(mockapi.QAID, "q-mfa", "value", "false")

	fmt.Println("3. apply the reviewed plan")
	p, err := nativepoc.Apply(ctx, c, plan)
	if err != nil {
		return err
	}
	fmt.Printf("   promotion %s: %s\n", p.ID, p.Status)

	fmt.Println("4. plan the rollback (reviewed and applied the same way)")
	rb, err := nativepoc.PlanRollback(ctx, c, plan)
	if err != nil {
		return err
	}
	return report(filepath.Join(dir, "2-rollback"), rb)
}
