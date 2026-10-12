package sandboxd

import "context"

// Main runs `bothy sandboxd`. Configuration comes only from BOTHY_SBX_* env.
func Main(ctx context.Context, args []string, env func() []string) error {
	<-ctx.Done()
	return nil
}
