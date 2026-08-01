package cli

// DefaultCommands returns the production command set.
func DefaultCommands() []Command {
	return []Command{
		searchCommand{},
		propertyCommand{},
		harCommand{},
		sessionCommand{},
		versionCommand{},
	}
}

type versionCommand struct {
	version string
}

func (versionCommand) Name() string    { return "version" }
func (versionCommand) Summary() string { return "Print the gozillo version" }
func (command versionCommand) Run(ctx Context, _ []string) error {
	version := command.version
	if version == "" {
		version = currentVersion()
	}
	_, err := ctx.Stdout.Write([]byte(Name + " " + version + "\n"))
	return err
}
