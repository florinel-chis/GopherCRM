"""Helpers the analysis components call. Components are self-contained classes (Langflow
rebuilds them from embedded source), so everything shared lives here as plain functions.

Trust boundary: a graph edge carries a run id and nothing else. Which configuration a node
runs under comes from the node's own input or the server environment, never from upstream
data, and the run is reloaded from its private record under that configuration."""

import os

from lfx.schema.data import Data


def config_path(value: str) -> str:
    from gophercrm_runner.config import GateError

    path = value or os.environ.get('GOPHERCRM_RUNNER_CONFIG', '')
    if not path:
        raise GateError('Set a local configuration path or GOPHERCRM_RUNNER_CONFIG in the Langflow server environment')
    return path


def load(path: str):
    from gophercrm_runner.analysis import Analysis
    from gophercrm_runner.config import Config

    return Analysis(Config.load(path))


def upstream_run_id(value) -> str:
    from gophercrm_runner.config import GateError

    data = value.data if isinstance(value, Data) else None
    run_id = data.get('run_id') if isinstance(data, dict) else None
    if not isinstance(run_id, str):
        raise GateError('upstream node did not provide an analysis stage output')
    return run_id


def as_data(output) -> Data:
    """What the next node sees: the run id to continue from and this stage's readable result
    for the inspector. Nothing in it is trusted downstream beyond the id."""
    return Data(data={'run_id': output.run.run_id, 'stage': output.stage, 'slug': output.run.slug,
                      'notes': list(output.notes), 'markdown': output.markdown})
