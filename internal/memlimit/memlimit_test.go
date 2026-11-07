package memlimit

import (
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/suite"
)

// TestMemlimit is the package's single entry point; every suite is registered here.
func TestMemlimit(t *testing.T) {
	suite.Run(t, new(configureSuite))
}

type configureSuite struct {
	suite.Suite

	env map[string]string
	set []int64 // limits passed to the setter, in order
}

func (s *configureSuite) SetupTest() {
	s.env = map[string]string{}
	s.set = nil
}

func (s *configureSuite) configure(fsys fstest.MapFS) (int64, error) {
	return configure(fsys, func(k string) string { return s.env[k] }, func(n int64) int64 {
		s.set = append(s.set, n)
		return 0
	})
}

// own cgroup namespace: /proc/self/cgroup names the root and the limit file is
// at the top of /sys/fs/cgroup — Docker's default on cgroup v2.
func namespaced(memoryMax string) fstest.MapFS {
	return fstest.MapFS{
		"proc/self/cgroup":         {Data: []byte("0::/\n")},
		"sys/fs/cgroup/memory.max": {Data: []byte(memoryMax + "\n")},
	}
}

func (s *configureSuite) TestSetsNinetyPercentOfTheContainerLimit() {
	limit, err := s.configure(namespaced("1073741824"))
	s.Require().NoError(err)
	s.Equal(int64(966367641), limit)
	s.Equal([]int64{966367641}, s.set)
}

func (s *configureSuite) TestFollowsTheCgroupPathInTheHostNamespace() {
	// cgroupns=host: the process sees the whole hierarchy and its own path.
	fsys := fstest.MapFS{
		"proc/self/cgroup":         {Data: []byte("0::/system.slice/docker-ab.scope\n")},
		"sys/fs/cgroup/memory.max": {Data: []byte("max\n")},
		"sys/fs/cgroup/system.slice/docker-ab.scope/memory.max": {Data: []byte("2147483648\n")},
	}
	limit, err := s.configure(fsys)
	s.Require().NoError(err)
	s.Equal(int64(1932735283), limit)
}

func (s *configureSuite) TestUnlimitedCgroupLeavesTheRuntimeAlone() {
	limit, err := s.configure(namespaced("max"))
	s.Require().NoError(err)
	s.Zero(limit)
	s.Empty(s.set)
}

func (s *configureSuite) TestExplicitGOMEMLIMITWins() {
	s.env["GOMEMLIMIT"] = "750MiB"
	limit, err := s.configure(namespaced("1073741824"))
	s.Require().NoError(err)
	s.Zero(limit)
	s.Empty(s.set, "the runtime already applied the operator's GOMEMLIMIT")
}

func (s *configureSuite) TestNoCgroupV2IsNotAnError() {
	// Bare metal, macOS, or cgroup v1: nothing to read, nothing to set.
	limit, err := s.configure(fstest.MapFS{})
	s.Require().NoError(err)
	s.Zero(limit)
	s.Empty(s.set)
}

func (s *configureSuite) TestMalformedLimitIsAnError() {
	_, err := s.configure(namespaced("lots"))
	s.Require().Error(err)
	s.Empty(s.set)
}
