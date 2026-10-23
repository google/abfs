# Contributing to ABFS

Thank you for your interest in **ABFS (Another Build File Service)**!

## Current Contribution Policy

We are still in the process of stabilizing our external release and validation pipelines. As a result, **we are not accepting direct code contributions or GitHub Pull Requests at this time.** Any Pull Requests opened directly against this repository will be closed automatically.

However, building a high-performance Android development ecosystem is a collaborative effort, and we actively welcome dialogue with partners and community members adopting ABFS.

## How to Get in Touch

If your organization is evaluating or adopting ABFS and you would like to report an issue, suggest a feature, or discuss potential contributions (such as bug fixes, kernel compatibility improvements, or new storage/cloud backend integrations), please reach out to us directly at:

📧 **[abfs@google.com](mailto:abfs@google.com)**

### Reporting Issues

When reaching out to report a bug or unexpected behavior, including the following details will help our team investigate quickly:

- **ABFS & CASFS Version:** Output of `abfs --version` (or the release tag/commit hash you are running).
- **Environment Details:** Linux distribution, host architecture, and kernel version (`uname -r`).
- **Steps to Reproduce:** Commands run and the workflow leading up to the issue.
- **Logs & Diagnostics:** Relevant error output, `abfs doctor` status, or kernel log excerpts (`dmesg` / `journalctl`) if related to `casfs`.

### Proposing Features or Collaborations

If you are interested in collaborating on new capabilities or upstreaming changes, please email **[abfs@google.com](mailto:abfs@google.com)** with a brief overview of your use case and proposed design so we can coordinate with you directly.

## Looking Ahead

We are actively gathering feedback from early adopters and partners, and we plan to revisit and expand our contribution model as our external CI and release pipelines mature.

## Community Guidelines

This project follows [Google's Open Source Community Guidelines](https://opensource.google/conduct/). We are committed to providing a welcoming, respectful, and constructive environment for all partners and developers.
