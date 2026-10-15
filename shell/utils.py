import asyncio
import datetime
import functools
import os
import time
import traceback


def print_with_time(content, trace=False):
    if trace:
        content = f'{content}\n{traceback.format_exc()}'
    os.system(f'echo "[{time.strftime("%Y-%m-%d %H:%M:%S", time.localtime())}] {content}"')


def get_date_time(timestamp):
    return datetime.datetime.fromtimestamp(timestamp).strftime('%Y-%m-%d %H:%M:%S')


def str_to_bool(value):
    if value is None:
        return False
    return value.lower() in ('true', '1')


async def async_wrap(func, *args, **kwargs):
    loop = asyncio.get_running_loop()
    pfunc = functools.partial(func, *args, **kwargs)
    return await loop.run_in_executor(None, pfunc)


def safe_run(func):
    try:
        loop = asyncio.get_event_loop()
    except RuntimeError:
        loop = asyncio.new_event_loop()
        asyncio.set_event_loop(loop)

    if loop.is_running():
        asyncio.ensure_future(asyncio.create_task(func))
    else:
        loop.run_until_complete(func)
