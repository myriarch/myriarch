// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package live_test

import "net"

type net_dialer struct{}

func (net_dialer) dial(addr string) (net.Conn, error) { return net.Dial("tcp", addr) }
