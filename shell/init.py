import os
import sys

import utils

MAIN_FILE_PATH = "/root/shell/main.py"

if __name__ == '__main__':
    os.system("/usr/local/bin/python -m pip install --upgrade pip")
    os.system("pip freeze --local | grep -v '^-e' | cut -d = -f 1 | xargs -n1 pip install -U")
    os.system("pip install gunicorn")
    with open(MAIN_FILE_PATH, mode='r', encoding='utf-8') as main_file:
        for line in main_file.readlines():
            if line.startswith('# dists:'):
                dists = line.split(':')[1]
                for dist in dists.split(','):
                    os.system(f"pip install {dist.strip()}")
                break

            if line.startswith('from') or line.startswith('import'):
                dist = line.split(' ')[1]
                if '.' in dist:
                    dist = dist.split('.')[0]
                os.system(f"pip install {dist}")
            else:
                break
    if utils.str_to_bool(os.getenv('RUN_TRIGGER')):
        os.execl(sys.executable, sys.executable, MAIN_FILE_PATH)
    else:
        os.system("gunicorn -w 4 -b 0.0.0.0:8889 main:app --chdir /root/shell")
