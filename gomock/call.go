// Copyright 2010 Google Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package gomock

import (
	"fmt"
	"reflect"
	"strconv"
	"runtime/debug"
	"strings"
)

// Call represents an expected call to a mock.
type Call struct {
	t TestReporter // for triggering test failures on invalid call setup

	receiver   interface{}   // the receiver of the method call
	method     string        // the name of the method
	methodType reflect.Type  // the type of the method
	args       []Matcher     // the args
	rets       []interface{} // the return values (if any)
	origin     string        // file and line number of call setup

	preReqs []*Call // prerequisite calls

	// Expectations
	minCalls, maxCalls int

	numCalls int // actual number made

	// Actions
	doFunc  reflect.Value
	setArgs map[int]reflect.Value
}

// AnyTimes allows the expectation to be called 0 or more times
func (c *Call) AnyTimes() *Call {
	c.minCalls, c.maxCalls = 0, 1e8 // close enough to infinity
	return c
}

// MinTimes requires the call to occur at least n times. If AnyTimes or MaxTimes have not been called, MinTimes also
// sets the maximum number of calls to infinity.
func (c *Call) MinTimes(n int) *Call {
	c.minCalls = n
	if c.maxCalls == 1 {
		c.maxCalls = 1e8
	}
	return c
}

// MaxTimes limits the number of calls to n times. If AnyTimes or MinTimes have not been called, MaxTimes also
// sets the minimum number of calls to 0.
func (c *Call) MaxTimes(n int) *Call {
	c.maxCalls = n
	if c.minCalls == 1 {
		c.minCalls = 0
	}
	return c
}

// Do declares the action to run when the call is matched.
// It takes an interface{} argument to support n-arity functions.
func (c *Call) Do(f interface{}) *Call {
	// TODO: Check arity and types here, rather than dying badly elsewhere.
	c.doFunc = reflect.ValueOf(f)
	return c
}

func (c *Call) Return(rets ...interface{}) *Call {
	mt := c.methodType
	if len(rets) != mt.NumOut() {
		c.t.Fatalf("wrong number of arguments to Return for %T.%v: got %d, want %d [%s]\n%s",
			c.receiver, c.method, len(rets), mt.NumOut(), c.origin, debug.Stack())
	}
	for i, ret := range rets {
		if got, want := reflect.TypeOf(ret), mt.Out(i); got == want {
			// Identical types; nothing to do.
		} else if got == nil {
			// Nil needs special handling.
			switch want.Kind() {
			case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Ptr, reflect.Slice:
				// ok
			default:
				c.t.Fatalf("argument %d to Return for %T.%v is nil, but %v is not nillable [%s]\n%s",
					i, c.receiver, c.method, want, c.origin, debug.Stack())
			}
		} else if got.AssignableTo(want) {
			// Assignable type relation. Make the assignment now so that the generated code
			// can return the values with a type assertion.
			v := reflect.New(want).Elem()
			v.Set(reflect.ValueOf(ret))
			rets[i] = v.Interface()
		} else {
			c.t.Fatalf("wrong type of argument %d to Return for %T.%v: %v is not assignable to %v [%s]\n%s",
				i, c.receiver, c.method, got, want, c.origin, debug.Stack())
		}
	}

	c.rets = rets
	return c
}

func (c *Call) Times(n int) *Call {
	c.minCalls, c.maxCalls = n, n
	return c
}

// SetArg declares an action that will set the nth argument's value,
// indirected through a pointer.
func (c *Call) SetArg(n int, value interface{}) *Call {
	if c.setArgs == nil {
		c.setArgs = make(map[int]reflect.Value)
	}
	mt := c.methodType
	// TODO: This will break on variadic methods.
	// We will need to check those at invocation time.
	if n < 0 || n >= mt.NumIn() {
		c.t.Fatalf("SetArg(%d, ...) called for a method with %d args [%s]\n%s",
			n, mt.NumIn(), c.origin, debug.Stack())
	}
	// Permit setting argument through an interface.
	// In the interface case, we don't (nay, can't) check the type here.
	at := mt.In(n)
	switch at.Kind() {
	case reflect.Ptr:
		dt := at.Elem()
		if vt := reflect.TypeOf(value); !vt.AssignableTo(dt) {
			c.t.Fatalf("SetArg(%d, ...) argument is a %v, not assignable to %v [%s]\n%s",
				n, vt, dt, c.origin, debug.Stack())
		}
	case reflect.Interface:
		// nothing to do
	default:
		c.t.Fatalf("SetArg(%d, ...) referring to argument of non-pointer non-interface type %v [%s]\n%s",
			n, at, c.origin, debug.Stack())
	}
	c.setArgs[n] = reflect.ValueOf(value)
	return c
}

// isPreReq returns true if other is a direct or indirect prerequisite to c.
func (c *Call) isPreReq(other *Call) bool {
	for _, preReq := range c.preReqs {
		if other == preReq || preReq.isPreReq(other) {
			return true
		}
	}
	return false
}

// After declares that the call may only match after preReq has been exhausted.
func (c *Call) After(preReq *Call) *Call {
	if c == preReq {
		c.t.Fatalf("A call isn't allowed to be it's own prerequisite")
	}
	if preReq.isPreReq(c) {
		c.t.Fatalf("Loop in call order: %v is a prerequisite to %v (possibly indirectly).", c, preReq)
	}

	c.preReqs = append(c.preReqs, preReq)
	return c
}

// Returns true if the minimum number of calls have been made.
func (c *Call) satisfied() bool {
	return c.numCalls >= c.minCalls
}

// Returns true iff the maximum number of calls have been made.
func (c *Call) exhausted() bool {
	return c.numCalls >= c.maxCalls
}

func (c *Call) String() string {
	args := make([]string, len(c.args))
	for i, arg := range c.args {
		args[i] = arg.String()
	}
	arguments := strings.Join(args, ", ")
	return fmt.Sprintf("%T.%v(%s) %s", c.receiver, c.method, arguments, c.origin)
}

// Tests if the given call matches the expected call.
// If yes, returns nil. If no, returns error with message explaining why it does not match.
func (c *Call) matches(args []interface{}) error {
	if len(args) != len(c.args) {
		return fmt.Errorf("Invalid number of arguments of call: %s. Set: %s, while this call takes: %s",
			c.origin, strconv.Itoa(len(args)), strconv.Itoa(len(c.args)))
	}
	for i, m := range c.args {
		if !m.Matches(args[i]) {
			return fmt.Errorf("The expected argument of index: %s of this call: %s did not match the actual argument.\nActual argument: %s, expected: %v\n",
				strconv.Itoa(i), c.origin, m, args[i])
		}
	}

	// Check that all prerequisite calls have been satisfied.
	for _, preReqCall := range c.preReqs {
		if !preReqCall.satisfied() {
			return fmt.Errorf("A prerequisite call was not satisfied:\n%v\nshould be called before:\n%v", preReqCall, c)
		}
	}

	return nil
}

// dropPrereqs tells the expected Call to not re-check prerequisite calls any
// longer, and to return its current set.
func (c *Call) dropPrereqs() (preReqs []*Call) {
	preReqs = c.preReqs
	c.preReqs = nil
	return
}

func (c *Call) call(args []interface{}) (rets []interface{}, action func()) {
	c.numCalls++

	// Actions
	if c.doFunc.IsValid() {
		doArgs := make([]reflect.Value, len(args))
		ft := c.doFunc.Type()
		for i := 0; i < len(args); i++ {
			if args[i] != nil {
				doArgs[i] = reflect.ValueOf(args[i])
			} else {
				// Use the zero value for the arg.
				doArgs[i] = reflect.Zero(ft.In(i))
			}
		}
		action = func() { c.doFunc.Call(doArgs) }
	}
	for n, v := range c.setArgs {
		reflect.ValueOf(args[n]).Elem().Set(v)
	}

	rets = c.rets
	if rets == nil {
		// Synthesize the zero value for each of the return args' types.
		mt := c.methodType
		rets = make([]interface{}, mt.NumOut())
		for i := 0; i < mt.NumOut(); i++ {
			rets[i] = reflect.Zero(mt.Out(i)).Interface()
		}
	}

	return
}

// InOrder declares that the given calls should occur in order.
func InOrder(calls ...*Call) {
	for i := 1; i < len(calls); i++ {
		calls[i].After(calls[i-1])
	}
}
