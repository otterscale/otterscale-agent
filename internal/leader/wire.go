package leader

import "github.com/google/wire"

func ProvideElector() (*Elector, error) {
	return NewElector(Config{})
}

var ProviderSet = wire.NewSet(ProvideElector)
