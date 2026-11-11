package recycle

import (
	"context"

	"golang.org/x/sys/unix"

	"github.com/Pukka922/edelweiss-ext-samba/internal/safefs"
)

// Empty removes everything in the recycle bin of the share, empty folders of any age included (it is the admin's own
// action), and keeps #recycle itself. A share without #recycle is as it should be. It stops when ctx ends; an entry that
// cannot be removed is an error of the result, and the rest is removed all the same.
func Empty(ctx context.Context, root safefs.Root, share string) error {
	fd, ok, err := openRecycle(root, share)
	if err != nil || !ok {
		return err
	}
	defer unix.Close(fd)
	_, err = remove(ctx, fd, nil, nil)
	return err
}
