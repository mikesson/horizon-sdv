# Security Policy

To report a security issue, please use [https://g.co/vulnz](https://g.co/vulnz).

We use g.co/vulnz for our intake and coordinate vulnerability disclosure through
the
[Google Vulnerability Reward Program](https://bughunters.google.com/about/rules/google-friends-program)
process.

Please do not report security vulnerabilities through public GitHub issues.

## Note on secrets

This repository contains no credentials. The ABFS license and any service-account
keys are supplied at deploy time (the license via the node pool's GCE metadata) and
must never be committed; `.gitignore` excludes `*-license.json`, `*.key`, and `*.pem`.
