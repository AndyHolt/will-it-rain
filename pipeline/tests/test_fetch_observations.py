from datetime import date, datetime

import pytest

from pipeline.components.fetch_observations import _format_datetime_range, _to_datetime


def test_format_datetime_range_open_forward():
    result = _format_datetime_range(datetime(2023, 5, 12), None)
    assert result == "2023-05-12T00:00:00Z.."


def test_format_datetime_range_open_backward():
    result = _format_datetime_range(None, datetime(2026, 5, 12))
    assert result == "..2026-05-12T00:00:00Z"


def test_format_datetime_range_closed():
    result = _format_datetime_range(datetime(2023, 5, 12), datetime(2026, 5, 12))
    assert result == "2023-05-12T00:00:00Z/2026-05-12T00:00:00Z"


def test_format_datetime_range_both_none_raises():
    with pytest.raises(TypeError):
        _format_datetime_range(None, None)


def test_to_datetime_passes_through_datetime():
    dt = datetime(2026, 1, 2, 3, 4, 5)
    assert _to_datetime(dt) is dt


def test_to_datetime_passes_through_none():
    assert _to_datetime(None) is None


def test_to_datetime_promotes_date_to_midnight():
    assert _to_datetime(date(2026, 1, 2)) == datetime(2026, 1, 2, 0, 0, 0)
