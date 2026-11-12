# Test on a Mac with Podman

This runs the server the way it runs on a server: the same container image,
read-only, as a non-root user, with the secrets delivered by the container
runtime. It takes about 20 minutes the first time, most of it the image
build.

The server only reads PingOne (with the read-only worker) and only writes to
GitLab (a branch and a merge request). Use a **sandbox source environment**
and a **scratch GitLab project** for the first tests.

## What you need

- A Mac (Apple silicon or Intel) with [Homebrew](https://brew.sh), about 10 GB
  free disk.
- From PingOne: the region (`NA`, `EU`, `CA`, `AP`, `AU` or `SG`), the ID of
  the environment that holds the worker application, and the worker's client
  ID and client secret. The worker needs **read** roles only.
- From GitLab: the address (for example `https://gitlab.example.com`), the
  project's numeric ID (project page, under the name), and a token that can
  create branches and merge requests in that project (a project access token
  with the Developer role and the `api` scope).
- The config repository must already have one settings file per
  environment at its root (`development.tfvars`, `testing.tfvars`, ...).
  The server needs the source environment's PingOne ID: from
  `pingone_environment_id` in its `.tfvars`, or from `sourceEnvironmentIds`
  in the configuration (step 5).

## 1. Install Podman and start its machine

```bash
brew install podman
```

```bash
podman machine init --cpus 4 --memory 4096 --disk-size 40
```

```bash
podman machine start
```

`podman info` should now print details without an error. Podman on a Mac runs
containers in a small Linux machine; it keeps running until you stop it
(`podman machine stop`).

### If you see "certificate signed by unknown authority"

Your network inspects HTTPS (a company proxy such as Zscaler or Netskope signs
every site with the company's own root certificate). The Mac trusts it; the
Podman machine and the image build do not yet. Do this once:

1. Save the company root certificate as a file.

   **Netskope:** the Netskope client keeps its certificates in a file; copy it:

   ```bash
   cp "/Library/Application Support/Netskope/STAgent/data/nscacert.pem" ~/company-root.crt
   ```

   If that file is not there, export them from the System keychain (they are
   named like `caadmin.netskope.com` and `ca.<tenant>.goskope.com`):

   ```bash
   security find-certificate -a -c "netskope" -p /Library/Keychains/System.keychain > ~/company-root.crt; security find-certificate -a -c "goskope" -p /Library/Keychains/System.keychain >> ~/company-root.crt
   ```

   **Other proxies:** find the root certificate's name in **Keychain
   Access** (search the **System** keychain for your proxy vendor or company,
   for example "Zscaler Root CA"), then:

   ```bash
   security find-certificate -a -c "Zscaler Root CA" -p /Library/Keychains/System.keychain > ~/company-root.crt
   ```

   Replace `Zscaler Root CA` with the name you found. The file must start
   with `-----BEGIN CERTIFICATE-----`. (Root certificates are public; this
   file is not a secret.)

2. Make the Podman machine trust it, then restart the machine:

   ```bash
   podman machine ssh "sudo tee /etc/pki/ca-trust/source/anchors/company-root.crt >/dev/null" < ~/company-root.crt
   ```

   ```bash
   podman machine ssh "sudo update-ca-trust" && podman machine stop && podman machine start
   ```

3. After step 2 below, copy it into the build so the image trusts it too
   (for the build's downloads and for the server's calls to PingOne and
   GitLab):

   ```bash
   cp ~/company-root.crt deploy/container/extra-ca/company-root.crt
   ```

If `podman machine init` itself fails with this error (before a machine
exists), the company certificate is not trusted by macOS for that download:
ask your IT team to install it as a trusted root in the System keychain, or
run `podman machine init` once on a network without the proxy.

## 2. Get the code

```bash
git clone https://github.com/ChayzX/pingone-promote.git && cd pingone-promote && git checkout release/hosted-v1
```

## 3. Build the image

```bash
podman build -f deploy/container/Containerfile -t pingone-promote:test --build-arg VERSION=$(git rev-parse --short HEAD) .
```

(If you use a company root certificate, copy it into
`deploy/container/extra-ca/` first; see "certificate signed by unknown
authority" above.) This downloads Go, Terraform 1.16.7 and pingcli-terraformer 0.6.0 for your
Mac's processor, checks each download against the checksum recorded in the
repository, and builds the server. It takes several minutes. Check it:

```bash
podman run --rm --entrypoint /opt/pingone-promote/pingone-promote pingone-promote:test --version
```

## 4. Read the tool checksums

The server refuses to run a tool whose checksum differs from the one in its
configuration. Print both:

```bash
podman run --rm --entrypoint sha256sum pingone-promote:test /opt/pingone-promote/tools/terraform /opt/pingone-promote/tools/pingcli-terraformer
```

Keep the two long values for the next step.

## 5. Write the configuration

```bash
mkdir -p ~/pingone-promote-test && cp deploy/container/config.podman.json ~/pingone-promote-test/config.json && open -e ~/pingone-promote-test/config.json
```

Change these values and save:

| Field | Put |
| --- | --- |
| `gitlab.baseUrl` | your GitLab address, `https://...`, nothing after the host |
| `gitlab.projectId` | the config repository's numeric project ID |
| `pingone.regionCode` | the region, for example `NA` |
| `pingone.workerEnvironmentId` | the ID of the environment that holds the worker application |
| `sources` | the environment(s) you copy from, named like their `.tfvars` file, for example `["development"]` |
| `sourceEnvironmentIds` | the PingOne environment ID of each source, for example `{"development": "<ID>"}` (PingOne admin console, Environment, Settings). Needed when your `.tfvars` files do not set `pingone_environment_id` (it comes from a GitLab CI variable); otherwise remove the line |
| `rollout` | the environments the pipeline applies to, in order, for example `["testing", "stage", "production"]` |
| `branchPrefix` | your branch prefix, for example `CIAM-` |
| `applyJobPattern` | the name of your pipeline's apply job with `{env}` where the environment name goes: for jobs named `terraform:apply-development`, `terraform:apply-stage`, ... use `terraform:apply-{env}` |
| `tools.terraformSha256` | the first value from step 4 |
| `tools.terraformerSha256` | the second value from step 4 |

Leave `listen`, `exposure` and `secretsFile` as they are: inside the container
the server listens on all interfaces, and `"exposure": "container"` makes it
answer only `localhost`. Step 7 publishes it to your Mac's own `127.0.0.1`,
so nothing else on your network can reach it.

## 6. Store the secrets in Podman

Create a file with exactly these three lines and your values (no quotes
needed):

```bash
cat > ~/pingone-promote-test/secrets.env <<'EOF'
PINGONE_PROMOTE_WORKER_CLIENT_ID=paste-the-worker-client-id
PINGONE_PROMOTE_WORKER_CLIENT_SECRET=paste-the-worker-client-secret
PINGONE_PROMOTE_GITLAB_TOKEN=paste-the-gitlab-token
EOF
```

Edit it to put the real values in (`open -e ~/pingone-promote-test/secrets.env`),
then hand it to Podman and delete the file:

```bash
podman secret create pingone-promote-secrets ~/pingone-promote-test/secrets.env && rm ~/pingone-promote-test/secrets.env
```

Podman gives the container a copy that only the server's user can read. To
change a value later: `podman secret rm pingone-promote-secrets`, then create
it again.

## 7. Check, then start

Check the configuration, the secrets and the tools (no PingOne or GitLab call
is made):

```bash
podman run --rm --read-only --tmpfs /tmp:rw,exec,nosuid,nodev --secret pingone-promote-secrets,target=secrets.env,uid=10001,gid=10001,mode=0400 -v ~/pingone-promote-test/config.json:/etc/pingone-promote/config.json:ro pingone-promote:test --check-config
```

It prints `configuration, secrets file and tools are usable`. Then start the
server:

```bash
podman run -d --name pingone-promote -p 127.0.0.1:8080:8080 --read-only --tmpfs /tmp:rw,exec,nosuid,nodev --secret pingone-promote-secrets,target=secrets.env,uid=10001,gid=10001,mode=0400 -v ~/pingone-promote-test/config.json:/etc/pingone-promote/config.json:ro pingone-promote:test
```

Open <http://localhost:8080/>.

## 8. Use it

1. **Start a new request**, type your name, choose the source environment.
2. **What to copy**: items you pick, whole sections (for example all of
   DaVinci, or all applications), or the whole environment. Press
   **Use this choice**.
3. **Find items**: search by name or ID, or leave the name empty to list
   everything. **Add** copies an item; **Leave out** keeps one out.
4. **Preview the change.** The first preview reads the whole source
   environment and can take a minute.
5. **Fill in what each environment needs** (URLs, IDs) and preview again
   until it says **Ready to submit**.
6. **Submit to GitLab** as a merge request or a branch only.
7. Follow the rollout on the **Requests** page after the merge.

Good first tests: one notification template alone; one application with its
dependencies; then the cases in `docs/acceptance/core-export.md`.

## Day to day

| To | Run |
| --- | --- |
| see the log (no secrets are ever logged) | `podman logs -f pingone-promote` |
| stop | `podman stop pingone-promote` |
| start again | `podman start pingone-promote` |
| update to a newer build | `git pull`, repeat step 3, then `podman rm -f pingone-promote` and the start command of step 7 |
| remove everything | `podman rm -f pingone-promote && podman secret rm pingone-promote-secrets && podman rmi pingone-promote:test` |

Drafts are kept in memory only: stopping the container discards unfinished
drafts. Submitted requests are in GitLab.

## Troubleshooting

| What you see | Why | Fix |
| --- | --- | --- |
| `Cannot connect to Podman` | the Podman machine is not running | `podman machine start` |
| `certificate signed by unknown authority` (machine, build, or PingOne/GitLab calls) | a company proxy inspects HTTPS | see "If you see certificate signed by unknown authority" under step 1 |
| `--check-config` says a checksum does not match | the values in `tools` are not the ones the image has | repeat step 4 and copy both values again |
| `the secrets file must ...` | the secret was not passed as in step 7 | use exactly `--secret pingone-promote-secrets,target=secrets.env,uid=10001,gid=10001,mode=0400` |
| `the secrets file must set ...` | a line is missing or misspelled | recreate the secret (step 6) with all three names |
| the browser cannot reach `localhost:8080` | the container stopped | `podman logs pingone-promote` shows why; fix and `podman start pingone-promote` |
| `421` / "not allowed" page | you opened it by another name, such as the Mac's IP | use `http://localhost:8080/` |
| search or preview fails with a PingOne error | wrong region, worker environment, client ID or secret, or missing read roles | check the values; the worker needs read access to every service you copy |
| `HTTP 403` notes that some item types were not read | the worker cannot read them, or the service is not licensed | expected for unlicensed services; otherwise add the read role |
| GitLab errors on preview | wrong address, project ID or token, or GitLab is only reachable on a VPN the Podman machine does not use | check the values; `podman machine ssh curl -sI https://your-gitlab` tests the route |
| "does not know the PingOne environment ID of the source" | your `.tfvars` do not set `pingone_environment_id` | add the source's ID under `sourceEnvironmentIds` (step 5), then `podman rm -f pingone-promote` and the start command of step 7 |
| preview says a value or ID is needed | another environment needs its own value | fill it in on the preview page; it is saved in that environment's `.tfvars` |
| "The default branch changed after the preview" | someone merged in between | press **Preview again**, then submit |
