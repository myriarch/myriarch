<!-- Entry for CHANGELOG.md, section Unreleased. It is folded into CHANGELOG.md when the next release is prepared. -->

### Changed

- **Artifact Hub: the metadata of the collector chart.** The workflow `artifacthub` asks for the chart (`myriarch` or `myriarch-collector`) next to the repository ID and writes the metadata file of the chosen chart repository, so the collector chart can show Verified publisher like the server chart. It refuses an ID that the other chart repository already carries. The workflow runs from the release tag, so the tags `v0.2.0` and `v0.2.1` keep the workflow with one input, which writes the server chart only; see [Releasing](docs/releasing.md#artifact-hub).
