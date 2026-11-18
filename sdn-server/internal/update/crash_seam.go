package update

// crashAfterHomeStateWrite is nil in every shipped build. Only a build with
// the sdn_e2e_crash_mid_apply tag sets it (crash_mid_apply_e2e.go): it ends the
// process right after a home install's state write and before trial.json is
// written, so the real-daemon E2E can prove the build still runs on trial.
var crashAfterHomeStateWrite func()
