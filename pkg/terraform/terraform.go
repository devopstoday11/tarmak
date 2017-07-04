package terraform

import (
	"fmt"

	"github.com/Sirupsen/logrus"

	tarmakDocker "github.com/jetstack/tarmak/pkg/docker"
	"github.com/jetstack/tarmak/pkg/tarmak/config"
)

var terraformDockerImageName = "jetstack/tarmak-terraform"

var terraformDockerfile = `
FROM alpine:3.6

RUN apk add --no-cache unzip curl

# install terraform
ENV TERRAFORM_VERSION 0.9.8
ENV TERRAFORM_HASH f951885f4e15deb4cf66f3b199964e3e74a0298bb46c9fe42e105df2ebcf3d16
RUN curl -sL  https://releases.hashicorp.com/terraform/${TERRAFORM_VERSION}/terraform_${TERRAFORM_VERSION}_linux_amd64.zip > /tmp/terraform.zip && \
    echo "${TERRAFORM_HASH}  /tmp/terraform.zip" | sha256sum  -c && \
    unzip /tmp/terraform.zip && \
    rm /tmp/terraform.zip && \
    mv terraform /usr/local/bin/terraform && \
    chmod +x /usr/local/bin/terraform
`

type Terraform struct {
	*tarmakDocker.App
	log    *logrus.Entry
	tarmak config.Tarmak
}

func New(tarmak config.Tarmak) *Terraform {
	log := tarmak.Log().WithField("module", "terraform")

	app := tarmakDocker.NewApp(
		tarmak,
		log,
		"jetstack/tarmak-terraform",
		"terraform",
	)

	return &Terraform{
		App:    app,
		log:    log,
		tarmak: tarmak,
	}
}

func (t *Terraform) NewContainer(stack *config.Stack) *TerraformContainer {
	c := &TerraformContainer{
		AppContainer: t.Container(),
		t:            t,
		log:          t.log.WithField("stack", stack.StackName()),
		stack:        stack,
	}
	return c
}

func (t *Terraform) Apply(stack *config.Stack) error {
	return t.planApply(stack, false)
}

func (t *Terraform) Destroy(stack *config.Stack) error {
	return t.planApply(stack, true)
}

func (t *Terraform) planApply(stack *config.Stack, destroy bool) error {
	c := t.NewContainer(stack)

	if err := c.prepare(); err != nil {
		return fmt.Errorf("error preparing container: %s", err)
	}

	initialStateStack := false
	// check for initial state run on first deployment
	if !destroy && stack.StackName() == config.StackNameState {
		remoteStateAvail, err := t.tarmak.Context().RemoteStateAvailable()
		if err != nil {
			return fmt.Errorf("error finding remote state: %s", err)
		}
		if !remoteStateAvail {
			initialStateStack = true
			c.log.Infof("running state stack for the first time, by passing remote state")
		}
	}

	if !initialStateStack {
		err := c.CopyRemoteState(t.tarmak.Context().RemoteState(stack.StackName()))

		if err != nil {
			return fmt.Errorf("error while copying remote state: %s", err)
		}
		c.log.Debug("copied remote state into container")
	}

	if err := c.Init(); err != nil {
		return fmt.Errorf("error while terraform init: %s", err)
	}

	// check for destroying the state stack
	if destroy && stack.StackName() == config.StackNameState {
		c.log.Infof("moving remote state to local")

		err := c.CopyRemoteState("")
		if err != nil {
			return fmt.Errorf("error while copying empty remote state: %s", err)
		}
		c.log.Debug("copied empty remote state into container")

		if err := c.InitForceCopy(); err != nil {
			return fmt.Errorf("error while terraform init -force-copy: %s", err)
		}
	}

	changesNeeded, err := c.Plan(destroy)
	if err != nil {
		return fmt.Errorf("error while terraform plan: %s", err)
	}

	if changesNeeded {
		if err := c.Apply(); err != nil {
			return fmt.Errorf("error while terraform apply: %s", err)
		}
	}

	// upload state if it was an inital state run
	if initialStateStack {
		err := c.CopyRemoteState(t.tarmak.Context().RemoteState(stack.StackName()))
		if err != nil {
			return fmt.Errorf("error while copying remote state: %s", err)
		}
		c.log.Debug("copied remote state into container")

		if err := c.InitForceCopy(); err != nil {
			return fmt.Errorf("error while terraform init -force-copy: %s", err)
		}
	}

	return nil
}