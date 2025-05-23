package docker

import (
	"context"
	"strconv"
	"testing"
	"time"

	dockertypes "github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/swarm"
	dockerclient "github.com/docker/docker/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeTasksClient struct {
	dockerclient.APIClient
	tasks     []swarm.Task
	container dockertypes.ContainerJSON
	err       error
}

func (c *fakeTasksClient) TaskList(ctx context.Context, options dockertypes.TaskListOptions) ([]swarm.Task, error) {
	return c.tasks, c.err
}

func (c *fakeTasksClient) ContainerInspect(ctx context.Context, container string) (dockertypes.ContainerJSON, error) {
	return c.container, c.err
}

func TestListTasks(t *testing.T) {
	testCases := []struct {
		service       swarm.Service
		tasks         []swarm.Task
		isGlobalSVC   bool
		expectedTasks []string
		networks      map[string]*network.Summary
	}{
		{
			service: swarmService(serviceName("container")),
			tasks: []swarm.Task{
				swarmTask("id1",
					taskSlot(1),
					taskNetworkAttachment("1", "network1", "overlay", []string{"127.0.0.1"}),
					taskStatus(taskState(swarm.TaskStateRunning)),
				),
				swarmTask("id2",
					taskSlot(2),
					taskNetworkAttachment("1", "network1", "overlay", []string{"127.0.0.2"}),
					taskStatus(taskState(swarm.TaskStatePending)),
				),
				swarmTask("id3",
					taskSlot(3),
					taskNetworkAttachment("1", "network1", "overlay", []string{"127.0.0.3"}),
				),
				swarmTask("id4",
					taskSlot(4),
					taskNetworkAttachment("1", "network1", "overlay", []string{"127.0.0.4"}),
					taskStatus(taskState(swarm.TaskStateRunning)),
				),
				swarmTask("id5",
					taskSlot(5),
					taskNetworkAttachment("1", "network1", "overlay", []string{"127.0.0.5"}),
					taskStatus(taskState(swarm.TaskStateFailed)),
				),
			},
			isGlobalSVC: false,
			expectedTasks: []string{
				"container.1",
				"container.4",
			},
			networks: map[string]*network.Summary{
				"1": {
					Name: "foo",
				},
			},
		},
	}

	for caseID, test := range testCases {
		t.Run(strconv.Itoa(caseID), func(t *testing.T) {
			t.Parallel()

			p := Provider{}
			dockerData, err := p.parseService(context.Background(), test.service, test.networks)
			require.NoError(t, err)

			dockerClient := &fakeTasksClient{tasks: test.tasks}
			taskDockerData, _ := listTasks(context.Background(), dockerClient, test.service.ID, dockerData, test.networks, test.isGlobalSVC)

			if len(test.expectedTasks) != len(taskDockerData) {
				t.Errorf("expected tasks %v, got %v", test.expectedTasks, taskDockerData)
			}

			for i, taskID := range test.expectedTasks {
				if taskDockerData[i].Name != taskID {
					t.Errorf("expect task id %v, got %v", taskID, taskDockerData[i].Name)
				}
			}
		})
	}
}

type fakeServicesClient struct {
	dockerclient.APIClient
	dockerVersion string
	networks      []network.Summary
	services      []swarm.Service
	tasks         []swarm.Task
	err           error
}

func (c *fakeServicesClient) ServiceList(ctx context.Context, options dockertypes.ServiceListOptions) ([]swarm.Service, error) {
	return c.services, c.err
}

func (c *fakeServicesClient) ServerVersion(ctx context.Context) (dockertypes.Version, error) {
	return dockertypes.Version{APIVersion: c.dockerVersion}, c.err
}

func (c *fakeServicesClient) NetworkList(ctx context.Context, options network.ListOptions) ([]network.Summary, error) {
	return c.networks, c.err
}

func (c *fakeServicesClient) TaskList(ctx context.Context, options dockertypes.TaskListOptions) ([]swarm.Task, error) {
	return c.tasks, c.err
}

func TestListServices(t *testing.T) {
	testCases := []struct {
		desc             string
		services         []swarm.Service
		tasks            []swarm.Task
		dockerVersion    string
		networks         []network.Summary
		expectedServices []string
	}{
		{
			desc: "Should return no service due to no networks defined",
			services: []swarm.Service{
				swarmService(
					serviceName("service1"),
					serviceLabels(map[string]string{
						"traefik.docker.network": "barnet",
						"traefik.docker.LBSwarm": "true",
					}),
					withEndpointSpec(modeVIP),
					withEndpoint(
						virtualIP("1", "10.11.12.13/24"),
						virtualIP("2", "10.11.12.99/24"),
					)),
				swarmService(
					serviceName("service2"),
					serviceLabels(map[string]string{
						"traefik.docker.network": "barnet",
						"traefik.docker.LBSwarm": "true",
					}),
					withEndpointSpec(modeDNSRR)),
			},
			dockerVersion:    "1.30",
			networks:         []network.Summary{},
			expectedServices: []string{},
		},
		{
			desc: "Should return only service1",
			services: []swarm.Service{
				swarmService(
					serviceName("service1"),
					serviceLabels(map[string]string{
						"traefik.docker.network": "barnet",
						"traefik.docker.LBSwarm": "true",
					}),
					withEndpointSpec(modeVIP),
					withEndpoint(
						virtualIP("yk6l57rfwizjzxxzftn4amaot", "10.11.12.13/24"),
						virtualIP("2", "10.11.12.99/24"),
					)),
				swarmService(
					serviceName("service2"),
					serviceLabels(map[string]string{
						"traefik.docker.network": "barnet",
						"traefik.docker.LBSwarm": "true",
					}),
					withEndpointSpec(modeDNSRR)),
			},
			dockerVersion: "1.30",
			networks: []network.Summary{
				{
					Name:       "network_name",
					ID:         "yk6l57rfwizjzxxzftn4amaot",
					Created:    time.Now(),
					Scope:      "swarm",
					Driver:     "overlay",
					EnableIPv6: false,
					Internal:   true,
					Ingress:    false,
					ConfigOnly: false,
					Options: map[string]string{
						"com.docker.network.driver.overlay.vxlanid_list": "4098",
						"com.docker.network.enable_ipv6":                 "false",
					},
					Labels: map[string]string{
						"com.docker.stack.namespace": "test",
					},
				},
			},
			expectedServices: []string{
				"service1",
			},
		},
		{
			desc: "Should return service1 and service2",
			services: []swarm.Service{
				swarmService(
					serviceName("service1"),
					serviceLabels(map[string]string{
						"traefik.docker.network": "barnet",
					}),
					withEndpointSpec(modeVIP),
					withEndpoint(
						virtualIP("yk6l57rfwizjzxxzftn4amaot", "10.11.12.13/24"),
						virtualIP("2", "10.11.12.99/24"),
					)),
				swarmService(
					serviceName("service2"),
					serviceLabels(map[string]string{
						"traefik.docker.network": "barnet",
					}),
					withEndpointSpec(modeDNSRR)),
			},
			tasks: []swarm.Task{
				swarmTask("id1",
					taskNetworkAttachment("yk6l57rfwizjzxxzftn4amaot", "network_name", "overlay", []string{"127.0.0.1"}),
					taskStatus(taskState(swarm.TaskStateRunning)),
				),
				swarmTask("id2",
					taskNetworkAttachment("yk6l57rfwizjzxxzftn4amaot", "network_name", "overlay", []string{"127.0.0.1"}),
					taskStatus(taskState(swarm.TaskStateRunning)),
				),
			},
			dockerVersion: "1.30",
			networks: []network.Summary{
				{
					Name:       "network_name",
					ID:         "yk6l57rfwizjzxxzftn4amaot",
					Created:    time.Now(),
					Scope:      "swarm",
					Driver:     "overlay",
					EnableIPv6: false,
					Internal:   true,
					Ingress:    false,
					ConfigOnly: false,
					Options: map[string]string{
						"com.docker.network.driver.overlay.vxlanid_list": "4098",
						"com.docker.network.enable_ipv6":                 "false",
					},
					Labels: map[string]string{
						"com.docker.stack.namespace": "test",
					},
				},
			},
			expectedServices: []string{
				"service1.0",
				"service1.0",
				"service2.0",
				"service2.0",
			},
		},
	}

	for _, test := range testCases {
		t.Run(test.desc, func(t *testing.T) {
			t.Parallel()

			dockerClient := &fakeServicesClient{services: test.services, tasks: test.tasks, dockerVersion: test.dockerVersion, networks: test.networks}

			p := Provider{}

			serviceDockerData, err := p.listServices(context.Background(), dockerClient)
			assert.NoError(t, err)

			assert.Equal(t, len(test.expectedServices), len(serviceDockerData))
			for i, serviceName := range test.expectedServices {
				if len(serviceDockerData) <= i {
					require.Fail(t, "index", "invalid index %d", i)
				}
				assert.Equal(t, serviceName, serviceDockerData[i].Name)
			}
		})
	}
}

func TestSwarmTaskParsing(t *testing.T) {
	testCases := []struct {
		service     swarm.Service
		tasks       []swarm.Task
		isGlobalSVC bool
		expected    map[string]dockerData
		networks    map[string]*network.Summary
	}{
		{
			service: swarmService(serviceName("container")),
			tasks: []swarm.Task{
				swarmTask("id1", taskSlot(1)),
				swarmTask("id2", taskSlot(2)),
				swarmTask("id3", taskSlot(3)),
			},
			isGlobalSVC: false,
			expected: map[string]dockerData{
				"id1": {
					Name: "container.1",
				},
				"id2": {
					Name: "container.2",
				},
				"id3": {
					Name: "container.3",
				},
			},
			networks: map[string]*network.Summary{
				"1": {
					Name: "foo",
				},
			},
		},
		{
			service: swarmService(serviceName("container")),
			tasks: []swarm.Task{
				swarmTask("id1"),
				swarmTask("id2"),
				swarmTask("id3"),
			},
			isGlobalSVC: true,
			expected: map[string]dockerData{
				"id1": {
					Name: "container.id1",
				},
				"id2": {
					Name: "container.id2",
				},
				"id3": {
					Name: "container.id3",
				},
			},
			networks: map[string]*network.Summary{
				"1": {
					Name: "foo",
				},
			},
		},
		{
			service: swarmService(
				serviceName("container"),
				withEndpointSpec(modeVIP),
				withEndpoint(
					virtualIP("1", ""),
				),
			),
			tasks: []swarm.Task{
				swarmTask(
					"id1",
					taskNetworkAttachment("1", "vlan", "macvlan", []string{"127.0.0.1"}),
					taskStatus(
						taskState(swarm.TaskStateRunning),
						taskContainerStatus("c1"),
					),
				),
			},
			isGlobalSVC: true,
			expected: map[string]dockerData{
				"id1": {
					Name: "container.id1",
					NetworkSettings: networkSettings{
						Networks: map[string]*networkData{
							"vlan": {
								Name: "vlan",
								Addr: "10.11.12.13",
							},
						},
					},
				},
			},
			networks: map[string]*network.Summary{
				"1": {
					Name: "vlan",
				},
			},
		},
	}

	for caseID, test := range testCases {
		t.Run(strconv.Itoa(caseID), func(t *testing.T) {
			t.Parallel()

			p := Provider{}

			dData, err := p.parseService(context.Background(), test.service, test.networks)
			require.NoError(t, err)

			for _, task := range test.tasks {
				taskDockerData := parseTasks(context.Background(), task, dData, test.networks, test.isGlobalSVC)
				expected := test.expected[task.ID]
				assert.Equal(t, expected.Name, taskDockerData.Name)
			}
		})
	}
}
