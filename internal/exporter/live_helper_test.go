package exporter

import (
	"bufio"
	"os"
	"strings"
	"testing"
)

func liveCreds(t *testing.T) Credentials {
	f, err := os.Open(os.Getenv("PINGONE_PROMOTE_LIVE_ENV"))
	if err != nil {
		t.Skip()
	}
	defer f.Close()
	v := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if k, x, ok := strings.Cut(sc.Text(), "="); ok {
			v[k] = x
		}
	}
	return Credentials{ClientID: v["PINGONE_PROMOTE_WORKER_CLIENT_ID"], ClientSecret: v["PINGONE_PROMOTE_WORKER_CLIENT_SECRET"], WorkerEnvironmentID: v["PINGONE_PROMOTE_WORKER_ENVIRONMENT_ID"], ExportEnvironmentID: v["PINGONE_PROMOTE_SOURCE_ENVIRONMENT_ID"], RegionCode: v["PINGONE_PROMOTE_REGION_CODE"]}
}
