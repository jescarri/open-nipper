# Minimal capabilities for APT in the sandbox

When the sandbox uses `--cap-drop ALL`, APT (apt-get update/install) fails unless specific capabilities are re-added. This document explains **why each capability is needed** and references the APT implementation and Linux capability semantics.

## Summary: five capabilities

| Capability      | Why APT needs it |
|-----------------|------------------|
| **CAP_SETUID**  | Method helper must call `setuid(2)`/`setresuid(2)` to become user `_apt`. |
| **CAP_SETGID** | Method helper must call `setgid(2)`/`setresgid(2)` and `setgroups(2)` to become group `_apt`. |
| **CAP_CHOWN**   | Root must `lchown(2)` partial dirs/files to `_apt:root` so the helper can use them after dropping privileges. |
| **CAP_FOWNER**  | Root (or _apt) must `chmod(2)` partial dirs/files; without FOWNER, chmod on files not owned by the process fails. |
| **CAP_DAC_OVERRIDE** | Helper running as _apt must unlink/remove files in partial dirs; if dirs are still root-owned or contain root-owned files, unlink needs DAC bypass. |

With **only** these five capabilities (and no others), APT’s built-in _apt sandbox works; no need for `APT::Sandbox::User "root"`.

---

## Source references

### APT privilege drop

- **DropPrivileges()** in `apt-pkg/contrib/fileutl.cc` (APT source, Salsa Debian):
  - Calls `setgroups(1, &pw->pw_gid)`, then `setresgid`/`setgid`/`setegid`, then `setresuid`/`setuid`/`seteuid` to become the sandbox user (e.g. `_apt`).
  - Ref: https://salsa.debian.org/apt-team/apt/-/blob/main/apt-pkg/contrib/fileutl.cc (search for `DropPrivileges`).

- **ChangeOwnerAndPermissionOfFile()** in the same file:
  - When `getuid() == 0`, calls `lchown(file, pw->pw_uid, gr->gr_gid)` (chown to _apt:root) then `chmod(file, mode)`.
  - Ref: same file, `ChangeOwnerAndPermissionOfFile` / `SetupAPTPartialDirectory` callers.

- **pkgAcqMethod::DropPrivsOrDie()** in `apt-pkg/acquire-method.cc`:
  - Calls `DropPrivileges()`; on failure the method exits with 112 (European emergency number).
  - Ref: https://salsa.debian.org/apt-team/apt/-/blob/main/apt-pkg/acquire-method.cc.

### Linux capabilities (why each cap)

- **capabilities(7)** – authoritative list of what each capability allows:
  - **CAP_SETUID**: “Make arbitrary manipulations of process UIDs (setuid(2), setreuid(2), setresuid(2), setfsuid(2))”.
  - **CAP_SETGID**: “Make arbitrary manipulations of process GIDs and supplementary GID list”.
  - **CAP_CHOWN**: “Make arbitrary changes to file UIDs and GIDs (chown(2))”.
  - **CAP_FOWNER**: “Bypass permission checks on operations that normally require the filesystem UID of the process to match the UID of the file (e.g., chmod(2), utime(2))”.
  - **CAP_DAC_OVERRIDE**: “Bypass file read, write, and execute permission checks”.
- Ref: `man 7 capabilities` or https://man7.org/linux/man-pages/man7/capabilities.7.html.

### Observed failures without these caps

- Without **SETUID/SETGID**: `setgroups 65534 failed`, `setegid 65534 failed`, `seteuid 42 failed` (Operation not permitted).
- Without **CHOWN**: `chown to _apt:root of directory ... failed` (Operation not permitted).
- Without **FOWNER**: `chmod 0700 of directory ... failed`, `chmod 0600 of file ... failed` (Operation not permitted).
- Without **DAC_OVERRIDE**: `rm: cannot remove '.../partial/*.deb': Permission denied`, `Problem unlinking the file ...` (Permission denied).

---

## Alternative: run APT as root

To avoid adding these capabilities, you can disable the _apt sandbox so APT runs entirely as root:

- Set `APT::Sandbox::User "root";` in `/etc/apt/apt.conf.d/` (e.g. `99-no-sandbox.conf`).
- Refs: Debian bug #769740, #903552; Ask Ubuntu “Download is performed unsandboxed as root”.

That removes the need for SETUID, SETGID, CHOWN, FOWNER, and DAC_OVERRIDE but weakens the in-container APT sandbox (downloads run as root). For an ephemeral, single-use sandbox, the five-capability set above is the minimal way to keep APT’s intended _apt sandbox behavior.
