import asyncio
import click

from pgrr import __version__
from pgrr import proxy
from pgrr import replay
from pgrr import patch_capture


@click.group(context_settings={"help_option_names": ["-h", "--help"]})
@click.version_option(version=__version__)
def cli():
    """pgrr - Postgres Record & Replay CLI"""
    pass


@cli.command()
@click.option("--port", "-p", default=5433, show_default=True, type=int,
              help="Port to listen on for the transparent proxy (clients connect here).")
@click.option("--upstream-host", default=proxy.REAL_PG_HOST, show_default=True, type=str,
              help="Upstream Postgres host that the proxy forwards to.")
@click.option("--upstream-port", default=proxy.REAL_PG_PORT, show_default=True, type=int,
              help="Upstream Postgres port that the proxy forwards to.")
def capture(port: int, upstream_host: str, upstream_port: int):
    """Start capture mode (runs the transparent proxy)."""
    click.echo(f"[capture] starting proxy on 0.0.0.0:{port}")
    click.echo(f"[capture] forwarding to upstream: {upstream_host}:{upstream_port}")
    click.echo(f"[capture] writing to capture file: {proxy.CAPTURE_FILE}")
    try:
        asyncio.run(proxy.listen(port, upstream_host, upstream_port))
    except KeyboardInterrupt:
        click.echo("\n[capture] shutting down...")
    finally:
        try:
            proxy.write_summary_record()
        except Exception:
            pass


@cli.command(name="replay")
@click.option("--speed", "-s", default=1.0, show_default=True, type=float,
              help="Replay speed multiplier (e.g., 2.0 = twice as fast).")
@click.option("--capture", "capture_path", default=replay.CAPTURE_FILE, show_default=True,
              type=str, help="Path to capture JSONL file.")
@click.option("--host", default=replay.TARGET_HOST, show_default=True, type=str,
              help="Target Postgres host.")
@click.option("--port", default=replay.TARGET_PORT, show_default=True, type=int,
              help="Target Postgres port.")
def replay_cmd(speed: float, capture_path: str, host: str, port: int):
    """Replay captured packets to the target Postgres."""
    if speed <= 0:
        raise click.UsageError("--speed must be > 0")
    click.echo(f"[replay] capture={capture_path} target={host}:{port} speed={speed}x")
    try:
        asyncio.run(replay.replay_all_sessions(capture_path, host, port, speed))
    except KeyboardInterrupt:
        click.echo("\n[replay] cancelled.")


@cli.command(name="patch-capture")
@click.argument("input", type=click.Path(exists=True))
@click.argument("output", required=False, default=None, type=click.Path())
@click.option("--db", default=None, help="Override target database name in startup packet.")
@click.option("--user", default=None, help="Override target user in startup packet.")
def patch_capture_cmd(input, output, db, user):
    """Rewrite database/user in a capture file's startup packet.

    \b
    Patch in-place (no OUTPUT argument):
      pgrr patch-capture queries.json --db testdb2

    Write to a new file:
      pgrr patch-capture queries.json patched.json --db testdb2
    """
    if not db and not user:
        raise click.UsageError("Specify at least --db or --user")
    patch_capture.patch_file(input, output, db, user)


if __name__ == "__main__":
    cli()
