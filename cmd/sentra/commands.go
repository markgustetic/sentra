package main

import (
	"os"

	"github.com/spf13/cobra"

	"github.com/markgustetic/sentra/internal/agent/action"
	"github.com/markgustetic/sentra/internal/cli"
)

// addProductionCommands wires every real command dependency. Tests build
// commands with stubs in internal/cli; this package is the production edge
// that touches S3, huh, the OS keyring, and the real LLM provider.
func addProductionCommands(root *cobra.Command, rootFlags *cli.RootFlags, version, commit string) {
	initPassphrase := promptInitPassphrase(rootFlags)
	setupPassphrase := promptSetupPassphrase(rootFlags)
	openPassphrase := promptOpenPassphraseWithConfig(rootFlags)

	root.AddCommand(cli.NewInit(cli.InitDeps{
		NewStore:   newS3Store,
		Passphrase: initPassphrase,
		Stdout:     os.Stdout,
	}))
	root.AddCommand(cli.NewSetup(cli.SetupDeps{
		NewStore:       newS3Store,
		Passphrase:     setupPassphrase,
		SavePassphrase: saveRepoPassphraseToKeyring,
		Stdout:         os.Stdout,
	}))
	root.AddCommand(cli.NewBackup(cli.BackupDeps{
		RepoDeps: cli.RepoDeps{
			NewStore:             newS3Store,
			PassphraseWithConfig: openPassphrase,
			Stdout:               os.Stdout,
		},
		Stderr:  os.Stderr,
		Confirm: cli.HuhBackupApplyConfirm,
	}))
	root.AddCommand(cli.NewSnapshots(cli.SnapshotsDeps{
		RepoDeps: cli.RepoDeps{
			NewStore:             newS3Store,
			PassphraseWithConfig: openPassphrase,
			Stdout:               os.Stdout,
		},
	}))
	root.AddCommand(cli.NewLs(cli.LsDeps{
		RepoDeps: cli.RepoDeps{
			NewStore:             newS3Store,
			PassphraseWithConfig: openPassphrase,
			Stdout:               os.Stdout,
		},
	}))
	pinDeps := cli.PinDeps{
		RepoDeps: cli.RepoDeps{
			NewStore:             newS3Store,
			PassphraseWithConfig: openPassphrase,
			Stdout:               os.Stdout,
		},
	}
	root.AddCommand(cli.NewPin(pinDeps))
	root.AddCommand(cli.NewUnpin(pinDeps))
	root.AddCommand(cli.NewStats(cli.StatsDeps{
		RepoDeps: cli.RepoDeps{
			NewStore:             newS3Store,
			PassphraseWithConfig: openPassphrase,
			Stdout:               os.Stdout,
		},
	}))
	root.AddCommand(cli.NewCheck(cli.CheckDeps{
		RepoDeps: cli.RepoDeps{
			NewStore:             newS3Store,
			PassphraseWithConfig: openPassphrase,
			Stdout:               os.Stdout,
		},
	}))
	root.AddCommand(cli.NewDoctor(cli.DoctorDeps{
		NewStore:             newS3Store,
		PassphraseWithConfig: openPassphrase,
		Stdout:               os.Stdout,
	}))
	root.AddCommand(cli.NewRecoveryKit(cli.RecoveryKitDeps{
		RepoDeps: cli.RepoDeps{
			NewStore:             newS3Store,
			PassphraseWithConfig: openPassphrase,
			Stdout:               os.Stdout,
		},
	}))
	root.AddCommand(cli.NewRestore(cli.RestoreDeps{
		RepoDeps: cli.RepoDeps{
			NewStore:             newS3Store,
			PassphraseWithConfig: openPassphrase,
			Stdout:               os.Stdout,
		},
		Stderr: os.Stderr,
	}))
	root.AddCommand(cli.NewDiff(cli.DiffDeps{
		RepoDeps: cli.RepoDeps{
			NewStore:             newS3Store,
			PassphraseWithConfig: openPassphrase,
			Stdout:               os.Stdout,
		},
	}))
	root.AddCommand(cli.NewPrune(cli.PruneDeps{
		RepoDeps: cli.RepoDeps{
			NewStore:             newS3Store,
			PassphraseWithConfig: openPassphrase,
			Stdout:               os.Stdout,
		},
		Confirm: cli.HuhConfirm,
	}))
	root.AddCommand(cli.NewPolicy(cli.PolicyDeps{
		RepoDeps: cli.RepoDeps{
			NewStore:             newS3Store,
			PassphraseWithConfig: openPassphrase,
			Stdout:               os.Stdout,
		},
		Stderr: os.Stderr,
	}))
	root.AddCommand(cli.NewSchedule(cli.ScheduleDeps{
		Stdout: os.Stdout,
	}))
	root.AddCommand(cli.NewPasswd(cli.PasswdDeps{
		NewStore:             newS3Store,
		PassphraseWithConfig: openPassphrase,
		NewPassphrase:        promptNewRepoPassphrase(),
		SavePassphrase:       saveRepoPassphraseToKeyring,
		DeletePassphrase:     deleteRepoPassphraseFromKeyring,
		Stdout:               os.Stdout,
	}))
	root.AddCommand(cli.NewSync(cli.SyncDeps{
		NewStore:             newS3Store,
		PassphraseWithConfig: openPassphrase,
		Stdout:               os.Stdout,
	}))
	root.AddCommand(cli.NewAgent(cli.AgentDeps{
		RepoDeps: cli.RepoDeps{
			NewStore:             newS3Store,
			PassphraseWithConfig: openPassphrase,
			Stdout:               os.Stdout,
		},
		ProviderForConfig: newAgentProvider,
		Heuristics:        defaultHeuristics(),
		Actions:           action.NewDefaultRegistry(),
		Confirm:           cli.HuhAgentConfirm,
	}))

	uiDeps := cli.UIDeps{
		RepoDeps: cli.RepoDeps{
			NewStore:             newS3Store,
			PassphraseWithConfig: openPassphrase,
			Stdout:               os.Stdout,
		},
		ProviderForConfig: newAgentProvider,
		Actions:           action.NewDefaultRegistry(),
		SavePassphrase:    saveRepoPassphraseToKeyring,
		Run:               cli.DefaultUIRunner,
		Version:           version,
		Commit:            commit,
		// The launch probe must honor --passphrase-file the same way the read
		// path does. RootFlags is populated by cobra as it parses argv, which
		// happens AFTER this wiring runs, so we hand the probe a func that reads
		// the live flag at run time rather than a pre-parse (empty) snapshot.
		PassphraseFile: func() string { return rootFlags.PassphraseFile },
	}
	root.AddCommand(cli.NewUI(uiDeps))
	cli.SetUIAsDefault(root, uiDeps)

	// `sentra local` reuses the very same uiDeps (SetupSeedConfig stays nil here
	// — the local command sets its own MinIO seed at run time) and wires the
	// production docker/health probe.
	root.AddCommand(cli.NewLocal(cli.LocalDeps{
		UI:          uiDeps,
		EnsureMinIO: ensureLocalMinIO,
	}))
}
