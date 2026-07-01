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
func addProductionCommands(root *cobra.Command, rootFlags *cli.RootFlags) {
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
		Run:               cli.DefaultUIRunner,
	}
	root.AddCommand(cli.NewUI(uiDeps))
	cli.SetUIAsDefault(root, uiDeps)
}
