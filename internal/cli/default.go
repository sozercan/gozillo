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

type versionCommand struct{}

func (versionCommand) Name() string    { return "version" }
func (versionCommand) Summary() string { return "Print the gozillo version" }
func (versionCommand) Run(ctx Context, _ []string) error {
	_, err := ctx.Stdout.Write([]byte("gozillo " + currentVersion() + "\n"))
	return err
}
