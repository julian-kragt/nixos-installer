# Setting up your config and secrets repository

nixos-installer works with two repositories of yours: the **config**, a NixOS flake with your hosts, and the **secrets
repository**, which holds the hosts' sops files. This guide shows what each needs. Examples use `myhost` as the host,
`alice` as its user, `~/nixos-config` and `~/nix-secrets` as the local clones, and `you` as the GitHub owner.

## The config

### nixos-bootstrap

Add [nixos-bootstrap](https://github.com/julian-kragt/nixos-bootstrap) to the config and list the host in its
`bootstrapEntries` output, as its README describes. The installer reads everything it needs to know about a host from
there:

| Output | Used for |
|---|---|
| `installerData.<host>` | the host list, the host's platform, its users, the disks the install wipes, root's SSH keys |
| `bootstrapEntries.<host>.hardwareConfigPath` | where the installer writes the hardware config |
| `nixosConfigurations.<host>-bootstrap` | the system nixos-anywhere installs |
| `nixosConfigurations.<host>` | the full system 3.2 switches to |

A host needs no hardware config before its first install: stage 0.1 generates the missing file from the target after
asking you (or without asking, with `--generate-hardware`), and 0.2 commits and pushes the one nixos-anywhere writes.

Check the entry before you start:

```sh
cd ~/nixos-config
nix eval --json .#installerData.myhost
```

### Disks

The install wipes every disk in the host's disko layout. Set the devices by hand, commit and push: the installer does
not pick a disk. Stage 0.1 fails when a device is not a block device on the target, and names the ones it is about to
wipe when it asks for your confirmation.

If the layout uses LUKS, read the passphrase from `/tmp/disko-password`:

```nix
luks = {
  type = "luks";
  name = "cryptroot";
  passwordFile = "/tmp/disko-password";
  # ...
};
```

The installer has you type the passphrase at the target's console, which writes it to that file on the ISO, in memory.
The bootstrap system has no SSH in its initrd, so someone types the passphrase at the console on every boot.

### The `nix-secrets` input

The config reads the secrets repository through a flake input that must be named `nix-secrets` and must be a
`git+ssh://` URL. It uses the config's `nixpkgs` and nixos-bootstrap, which checks its `secretsEntry`:

```nix
inputs.nix-secrets = {
  url = "git+ssh://git@github.com/you/nix-secrets.git?ref=main&shallow=1";
  inputs.nixpkgs.follows = "nixpkgs";
  inputs.nixos-bootstrap.follows = "nixos-bootstrap";
};
```

Stage 3.1 updates this input to the commit with the host's secrets, then commits and pushes `flake.lock`. The target
fetches it over SSH, with the provider's ssh-agent forwarded for the rebuild only, so the target never holds a key to
your repositories.

### sops-nix

The installer creates one age key for the host and one for each user on the host (stages 1.2 and 1.3) and encrypts the
files to them. Point sops-nix at those keys, and only those:

Take the paths from the secrets repository's layout, through nixos-bootstrap, so they follow its `secretsEntry` output:

```nix
secretsLayout = inputs.nixos-bootstrap.lib.secretsLayout { inherit lib; secretsFlake = inputs.nix-secrets; };
```

```nix
sops = {
  defaultSopsFile = "${inputs.nix-secrets}/${secretsLayout.hostFile config.networking.hostName}";
  validateSopsFiles = false;
  age = {
    keyFile = "/var/lib/sops-nix/key.txt"; # the host key, created by the installer
    sshKeyPaths = [ ];                     # never the SSH host key
    generateKey = false;                   # a new key would decrypt nothing
  };
};
```

Login passwords come from the user file whose template has `hashedPassword` (`passwordFile`; `null` when neither
has). The installer finds the users
whose password comes from sops by the secret's name, `passwords/<user>`, and checks after the switch that sops-nix
decrypted it:

```nix
sops.secrets."passwords/${user}" = {
  sopsFile = "${inputs.nix-secrets}/${secretsLayout.passwordFile hostName user}";
  key = "hashedPassword";
  neededForUsers = true;
};
users.users.${user}.hashedPasswordFile = config.sops.secrets."passwords/${user}".path;
```

For Home Manager, a user's own key is at `~/.config/sops/age/keys.txt`. `homeFile` is the user's shared file where
there is one, else their file on the host:

```nix
sops = {
  defaultSopsFile = "${inputs.nix-secrets}/${secretsLayout.homeFile hostName config.home.username}";
  age.keyFile = "${config.home.homeDirectory}/.config/sops/age/keys.txt";
  validateSopsFiles = false;
};
```

Home Manager is optional. When a user has a `home-manager-<user>.service`, 3.2 checks that it did not fail.

### The host's name

The installer names the host's files after the host's name in the flake (`nixosConfigurations.<host>`). The examples
above find them by `networking.hostName`, so keep the two the same, or use the flake's name in the paths.

### The repository

Keep the config in a local git clone with an ssh `origin` (`git@github.com:you/nixos-config.git` or
`ssh://git@github.com/you/nixos-config.git`) and a branch that tracks its upstream. The installer commits and pushes to
it (the hardware config, then `flake.lock`), so it refuses to start while the branch has uncommitted changes or is
ahead of or behind its upstream. The target builds only the pushed revision.

## The secrets repository

Start from [nix-secrets-reference](https://github.com/julian-kragt/nix-secrets-reference): copy it (don't fork it)
into a **private** repository and clone that. Its `DESIGN.md` explains the layout in depth.

The `secretsEntry` output of its `flake.nix` declares where each kind of file is. The installer reads it, checked, from
the flake's `installerSecretsData` output; it must be committed. With the reference's values:

```
.sops.yaml                      which recipients decrypt which file
flake.nix                       secretsEntry, the layout: userMode, directories, templates
hosts/<host>.yaml               the host's own secrets            → the host's key
host-users/<host>-<user>.yaml   a user's secrets on one host      → the host's key + the user's key on that host
templates/host.yaml             the keys every host file holds
templates/user.yaml             the keys every user file holds
```

With `userMode = "shared"` (or `"both"`), a user also has one file for all their hosts, `<sharedUserPath>/<user>.yaml`,
encrypted to each of those hosts and the user's key on each. When a host joins, the file is encrypted to it on a host
that already reads it (see `--rekey-via`).

### Templates

The templates are the schema of each kind of file. When the installer writes a file, it opens its template in vim
at the target's console. Each leaf is one of:

| Leaf | Filled in by |
|---|---|
| named `hashedPassword` | a password prompt; stored as a yescrypt hash, never in plain text |
| `CHANGEME` or `change_me` (in any case), or empty | you, in vim |
| anything else | the template; kept unless you change it |

Leaves can nest to any depth and can be multi-line strings or lists. The top-level key `sops` is reserved. Comments
are dropped from the written file, so they are never encrypted into it. A minimal pair:

```yaml
# templates/host.yaml: sops-nix needs the host's defaultSopsFile to exist
placeholder: unused
```

```yaml
# templates/user.yaml
hashedPassword: CHANGEME
```

A file that no longer has its template's keys, for example after you add a key to a template, is written again the next
time the secrets phase runs for its host.

### `.sops.yaml`

The installer writes `.sops.yaml` itself: an anchor per key and one rule per file.

```yaml
keys:
  - &host_myhost age1...
  - &user_alice_myhost age1...
creation_rules:
  - path_regex: ^hosts/myhost\.yaml$
    key_groups:
      - age:
          - *host_myhost
  - path_regex: ^host-users/myhost-alice\.yaml$
    key_groups:
      - age:
          - *host_myhost
          - *user_alice_myhost
```

- It edits the YAML structurally and keeps your other hosts, comments, anchors and rules.
- `keys` and `creation_rules` must be flat lists.
- sops uses the first rule that matches a file. The installer refuses to go on while another rule comes before one of
  the host's rules and also matches its file. A new rule that has to go before an existing rule's `path_regex` is added
  only after you type `add`.
- A recipient written literally into a rule, not by alias, is kept. That is how to add an extra recipient by hand.

There is no operator key: no recipient is added to every file, and the provider can't decrypt anything. A host's
secrets are read and edited on that host.

### Names

So that every file, anchor and rule names exactly one host and user, the secrets phase only accepts:

| Name | Pattern |
|---|---|
| host | `^[A-Za-z0-9][A-Za-z0-9-]*$` (no `_`) |
| user | `^[a-z_][a-z0-9_]*$` (no `-`, no uppercase, no leading digit) |

### The repository

Like the config, keep a local clone with a tracked upstream. The installer commits only `.sops.yaml` and the host's own
files, and refuses to start while anything else is uncommitted or the branch is not level with its upstream.

## Check your setup

```sh
cd ~/nixos-config
nix eval --json .#installerData --apply builtins.attrNames                                         # hosts the installer offers
nix eval --raw .#nixosConfigurations.myhost-bootstrap.config.system.build.toplevel.drvPath        # the bootstrap system evaluates
nix flake metadata --json | jq '.locks.nodes[.locks.nodes.root.inputs."nix-secrets"].locked.type' # "git"
git -C ~/nixos-config status -sb && git -C ~/nix-secrets status -sb                               # clean, level with upstream
ssh-add -l                                                                                         # a key from rootAuthorizedKeys
```

Then go on with [usage.md](usage.md).
