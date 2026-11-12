# Extra trusted certificates (company HTTPS inspection)

If your network inspects HTTPS (Zscaler, Netskope and similar), downloads fail
with "certificate signed by unknown authority". Put your company's root
certificate here as a PEM file ending in `.crt`, for example `company-root.crt`,
then build the image again. The build stage trusts it for its downloads, and
the running server trusts it for PingOne and GitLab. Files here are never
committed (see `.gitignore`); certificates are public, but keep this folder for
your own build.

See `docs/podman-mac.md`, "If you see certificate signed by unknown authority".
