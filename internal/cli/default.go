package cli

// Version is the CLI version. Release builds override it with -ldflags -X.
var Version = "0.1.0"

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
	_, err := ctx.Stdout.Write([]byte("gozillo " + Version + "\n"))
	return err
}
