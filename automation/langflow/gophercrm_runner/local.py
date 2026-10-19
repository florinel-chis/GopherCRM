"""Local Langflow setup. Credentials and execution state stay outside the checkout."""

import base64
from importlib.resources import files
import json
import os
from pathlib import Path
import secrets
import shutil

from .config import Config, GateError


def private_write(path, text):
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(fd, 'w') as out:
        out.write(text)


def setup(checkout: Path, directory: Path):
    checkout, directory = checkout.expanduser().resolve(), directory.expanduser().resolve()
    policy = json.loads(files('gophercrm_runner').joinpath('config.example.json').read_text())
    policy.update(checkout=str(checkout), state_dir=str(directory / 'state'))
    directory.mkdir(parents=True, exist_ok=True, mode=0o700)
    directory.chmod(0o700)
    policy_path = directory / 'config.json'
    if not policy_path.exists():
        private_write(policy_path, json.dumps(policy, indent=2) + '\n')
    Config.load(policy_path)
    env_path = directory / 'server.env'
    if not env_path.exists():
        values = {
            'LANGFLOW_CONFIG_DIR': str(directory / 'app'),
            'LANGFLOW_AUTO_LOGIN': 'false', 'LANGFLOW_SUPERUSER': 'runner',
            'LANGFLOW_SUPERUSER_PASSWORD': secrets.token_urlsafe(32),
            'LANGFLOW_SECRET_KEY': base64.urlsafe_b64encode(secrets.token_bytes(32)).decode(),
            'LANGFLOW_ENABLE_SIGNUP': 'false', 'LANGFLOW_CUSTOM_COMPONENT_ADMIN_ONLY': 'true',
            'LANGFLOW_DO_NOT_TRACK': 'true', 'GOPHERCRM_RUNNER_CONFIG': str(policy_path),
        }
        private_write(env_path, ''.join(k + '=' + json.dumps(v) + '\n' for k, v in values.items()))
    print(f'Configuration: {policy_path}\nPrivate Langflow login settings: {env_path}')


def serve(directory: Path, port: int):
    directory = directory.expanduser().resolve()
    if not (directory / 'server.env').is_file() or not 1 <= port <= 65535:
        raise GateError('Run setup first and provide a valid TCP port')
    executable = shutil.which('langflow')
    if not executable:
        raise GateError('Install the ui extra before starting the Langflow server')
    os.execv(executable, [executable, 'run', '--host', '127.0.0.1', '--port', str(port),
                         '--workers', '1', '--worker-timeout', '86400',
                         '--env-file', str(directory / 'server.env'), '--no-open-browser'])
