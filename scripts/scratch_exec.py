"""Private executable copies for macOS runtime tests (no TCC grants needed)."""
import pathlib
import shutil


def assert_safe_executable(path, home=None):
    path = pathlib.Path(path)
    assert path.is_absolute(), "test executable must be absolute"
    resolved = path.resolve(strict=True)
    home = pathlib.Path.home() if home is None else pathlib.Path(home)
    for name in ("Documents", "Desktop", "Downloads"):
        protected = home / name
        assert not path.is_relative_to(protected), "test executable is in a privacy-protected directory"
        assert not resolved.is_relative_to(protected.resolve()), "test executable resolves into a privacy-protected directory"
    return resolved


def copy_executable(source, scratch, name):
    scratch = pathlib.Path(scratch).resolve(strict=True)
    destination = scratch / "bin" / name
    destination.parent.mkdir(mode=0o700, exist_ok=True)
    # Refuse a pre-existing symlink before copying, including escapes from scratch.
    assert not destination.exists() and not destination.is_symlink(), "test executable already exists"
    shutil.copyfile(source, destination)
    destination.chmod(0o700)
    resolved = assert_safe_executable(destination)
    assert resolved.is_relative_to(scratch), "test executable escaped scratch"
    return str(resolved)
