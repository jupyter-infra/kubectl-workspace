/*
Copyright (c) Amazon Web Services
Distributed under the terms of the MIT license
*/

package tunnel

import "syscall"

// errConnReset is the error a read returns after a TCP reset; Winsock reports its
// own code, which does not compare equal to ECONNRESET.
var errConnReset error = syscall.WSAECONNRESET
