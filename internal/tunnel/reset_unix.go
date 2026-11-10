//go:build !windows

/*
Copyright (c) Amazon Web Services
Distributed under the terms of the MIT license
*/

package tunnel

import "syscall"

// errConnReset is the error a read returns after a TCP reset.
var errConnReset error = syscall.ECONNRESET
