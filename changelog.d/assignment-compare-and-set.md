<!-- Entry for CHANGELOG.md, section Unreleased. It is folded into CHANGELOG.md when the next release is prepared. -->

### Upgrade notes

- **Fleet assignments are written as a compare-and-set; a rolling update from v0.1.0 needs nothing special.** The assignment service writes an agent's assignment only if the assignment in the database is still the one it read, and records the fleet change and resets the desired state (and with it `failed_config_hash`) only when that write took effect. This needs no migration and no new column, so v0.1.0 and the new release can run side by side during a rolling update. Replicas of v0.1.0 still write unconditionally: while they run, two replicas can still record one fleet change twice, as before. The protection is complete when all replicas run this version.

### Fixed

- **A fleet change is recorded once with several replicas.** Two replicas that evaluated the same agent at the same time (`AssignAgent` on one, `ReassignAll` on the other) could both write the assignment, record the fleet change twice and reset the desired state twice, which also cleared `failed_config_hash`. The assignment is now a compare-and-set in the database. `AssignAgent` reads the snapshot after it has the agent's lock, a listing older than five minutes is treated as stale, and pruning of the record of recent writes is throttled instead of a full scan under the lock on every write.
