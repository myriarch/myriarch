<!-- Entry for CHANGELOG.md, section Unreleased. It is folded into CHANGELOG.md when the next release is prepared. -->

### Security

- **Windows installer: data directory reset.** Before it resets the permissions and owners in `C:\ProgramData\Myriarch`, the installer stops with `directory_unsafe` if a file there has more than one hard link, so that the reset only changes files of the data directory. The supervisor's service account is accepted as owner only below its two data directories, not for the directories themselves.
