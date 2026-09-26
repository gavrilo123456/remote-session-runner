// Package dispatcher contains the Mac execution router and its target drivers.
// The local driver sends only committed intent identity data to runner-locald;
// it never carries request payload or script bytes across the private API.
package dispatcher
