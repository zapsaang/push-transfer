def failed_response(message):
    return {
        "status": "failed",
        "message": message
    }


def success_response(message=None):
    return {
        "status": "success",
        "message": message
    }
